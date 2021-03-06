package oss

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
)

//
// CopyFile multipart copy object
//
// srcBucketName  Source bucket name
// srcObjectKey   Source object name
// destObjectKey   Target object name in the form of bucketname.objectkey
// partSize   part size in byte.
// options    Object's contraints. Check out function InitiateMultipartUpload。
//
// error Error is nill if the operation succeeds, otherwise it's the error object.
//
func (bucket Bucket) CopyFile(srcBucketName, srcObjectKey, destObjectKey string, partSize int64, options ...Option) error {
	destBucketName := bucket.BucketName
	if partSize < MinPartSize || partSize > MaxPartSize {
		return errors.New("oss: part size invalid range (1024KB, 5GB]")
	}

	cpConf, err := getCpConfig(options, filepath.Base(destObjectKey))
	if err != nil {
		return err
	}

	routines := getRoutines(options)

	if cpConf.IsEnable {
		return bucket.copyFileWithCp(srcBucketName, srcObjectKey, destBucketName, destObjectKey,
			partSize, options, cpConf.FilePath, routines)
	}

	return bucket.copyFile(srcBucketName, srcObjectKey, destBucketName, destObjectKey,
		partSize, options, routines)
}

// ----- Concurrently copy without checkpoint ---------

// copy worker arguments
type copyWorkerArg struct {
	bucket        *Bucket
	imur          InitiateMultipartUploadResult
	srcBucketName string
	srcObjectKey  string
	options       []Option
	hook          copyPartHook
}

// Hook for testing purpose
type copyPartHook func(part copyPart) error

var copyPartHooker copyPartHook = defaultCopyPartHook

func defaultCopyPartHook(part copyPart) error {
	return nil
}

// copy worker
func copyWorker(id int, arg copyWorkerArg, jobs <-chan copyPart, results chan<- UploadPart, failed chan<- error, die <-chan bool) {
	for chunk := range jobs {
		if err := arg.hook(chunk); err != nil {
			failed <- err
			break
		}
		chunkSize := chunk.End - chunk.Start + 1
		part, err := arg.bucket.UploadPartCopy(arg.imur, arg.srcBucketName, arg.srcObjectKey,
			chunk.Start, chunkSize, chunk.Number, arg.options...)
		if err != nil {
			failed <- err
			break
		}
		select {
		case <-die:
			return
		default:
		}
		results <- part
	}
}

// copy scheduler
func copyScheduler(jobs chan copyPart, parts []copyPart) {
	for _, part := range parts {
		jobs <- part
	}
	close(jobs)
}

// copy part structure
type copyPart struct {
	Number int   // part number (from 1 to 10,000)
	Start  int64 // the start index in the source file.
	End    int64 // the end index in the source file
}

// calculates copy parts
func getCopyParts(bucket *Bucket, objectKey string, partSize int64) ([]copyPart, error) {
	meta, err := bucket.GetObjectDetailedMeta(objectKey)
	if err != nil {
		return nil, err
	}

	parts := []copyPart{}
	objectSize, err := strconv.ParseInt(meta.Get(HTTPHeaderContentLength), 10, 0)
	if err != nil {
		return nil, err
	}

	part := copyPart{}
	i := 0
	for offset := int64(0); offset < objectSize; offset += partSize {
		part.Number = i + 1
		part.Start = offset
		part.End = GetPartEnd(offset, objectSize, partSize)
		parts = append(parts, part)
		i++
	}
	return parts, nil
}

// gets the source file size
func getSrcObjectBytes(parts []copyPart) int64 {
	var ob int64
	for _, part := range parts {
		ob += (part.End - part.Start + 1)
	}
	return ob
}

// concurrently copy without checkpoint
func (bucket Bucket) copyFile(srcBucketName, srcObjectKey, destBucketName, destObjectKey string,
	partSize int64, options []Option, routines int) error {
	descBucket, err := bucket.Client.Bucket(destBucketName)
	srcBucket, err := bucket.Client.Bucket(srcBucketName)
	listener := getProgressListener(options)

	// get copy parts
	parts, err := getCopyParts(srcBucket, srcObjectKey, partSize)
	if err != nil {
		return err
	}

	// initialize the multipart upload
	imur, err := descBucket.InitiateMultipartUpload(destObjectKey, options...)
	if err != nil {
		return err
	}

	jobs := make(chan copyPart, len(parts))
	results := make(chan UploadPart, len(parts))
	failed := make(chan error)
	die := make(chan bool)

	var completedBytes int64
	totalBytes := getSrcObjectBytes(parts)
	event := newProgressEvent(TransferStartedEvent, 0, totalBytes)
	publishProgress(listener, event)

	// start copy workers
	arg := copyWorkerArg{descBucket, imur, srcBucketName, srcObjectKey, options, copyPartHooker}
	for w := 1; w <= routines; w++ {
		go copyWorker(w, arg, jobs, results, failed, die)
	}

	// starts the scheduler
	go copyScheduler(jobs, parts)

	// waits for the parts finished.
	completed := 0
	ups := make([]UploadPart, len(parts))
	for completed < len(parts) {
		select {
		case part := <-results:
			completed++
			ups[part.PartNumber-1] = part
			completedBytes += (parts[part.PartNumber-1].End - parts[part.PartNumber-1].Start + 1)
			event = newProgressEvent(TransferDataEvent, completedBytes, totalBytes)
			publishProgress(listener, event)
		case err := <-failed:
			close(die)
			descBucket.AbortMultipartUpload(imur)
			event = newProgressEvent(TransferFailedEvent, completedBytes, totalBytes)
			publishProgress(listener, event)
			return err
		}

		if completed >= len(parts) {
			break
		}
	}

	event = newProgressEvent(TransferCompletedEvent, completedBytes, totalBytes)
	publishProgress(listener, event)

	// complete the multipart upload
	_, err = descBucket.CompleteMultipartUpload(imur, ups)
	if err != nil {
		bucket.AbortMultipartUpload(imur)
		return err
	}
	return nil
}

// ----- Concurrently copy with checkpoint  -----

const copyCpMagic = "84F1F18C-FF1D-403B-A1D8-9DEB5F65910A"

type copyCheckpoint struct {
	Magic          string       // magic
	MD5            string       // cp content MD5
	SrcBucketName  string       // source bucket
	SrcObjectKey   string       // source object
	DestBucketName string       // target bucket
	DestObjectKey  string       // target object
	CopyID         string       // copy id
	ObjStat        objectStat   // object stat
	Parts          []copyPart   // copy parts
	CopyParts      []UploadPart // the uploaded parts
	PartStat       []bool       // the part status
}

// Checks if the data is valid which means CP is valid and object is not updated.
func (cp copyCheckpoint) isValid(bucket *Bucket, objectKey string) (bool, error) {
	// compare CP's magic number and the MD5.
	cpb := cp
	cpb.MD5 = ""
	js, _ := json.Marshal(cpb)
	sum := md5.Sum(js)
	b64 := base64.StdEncoding.EncodeToString(sum[:])

	if cp.Magic != downloadCpMagic || b64 != cp.MD5 {
		return false, nil
	}

	// makes sure the object is not updated.
	meta, err := bucket.GetObjectDetailedMeta(objectKey)
	if err != nil {
		return false, err
	}

	objectSize, err := strconv.ParseInt(meta.Get(HTTPHeaderContentLength), 10, 0)
	if err != nil {
		return false, err
	}

	// Compares the object size and last modified time and etag.
	if cp.ObjStat.Size != objectSize ||
		cp.ObjStat.LastModified != meta.Get(HTTPHeaderLastModified) ||
		cp.ObjStat.Etag != meta.Get(HTTPHeaderEtag) {
		return false, nil
	}

	return true, nil
}

// load from the checkpoint file
func (cp *copyCheckpoint) load(filePath string) error {
	contents, err := ioutil.ReadFile(filePath)
	if err != nil {
		return err
	}

	err = json.Unmarshal(contents, cp)
	return err
}

// update the parts status
func (cp *copyCheckpoint) update(part UploadPart) {
	cp.CopyParts[part.PartNumber-1] = part
	cp.PartStat[part.PartNumber-1] = true
}

// dump the cp to the file
func (cp *copyCheckpoint) dump(filePath string) error {
	bcp := *cp

	// calculates MD5
	bcp.MD5 = ""
	js, err := json.Marshal(bcp)
	if err != nil {
		return err
	}
	sum := md5.Sum(js)
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	bcp.MD5 = b64

	// serialization
	js, err = json.Marshal(bcp)
	if err != nil {
		return err
	}

	// dum
	return ioutil.WriteFile(filePath, js, FilePermMode)
}

// unfinished parts
func (cp copyCheckpoint) todoParts() []copyPart {
	dps := []copyPart{}
	for i, ps := range cp.PartStat {
		if !ps {
			dps = append(dps, cp.Parts[i])
		}
	}
	return dps
}

// finished bytes count
func (cp copyCheckpoint) getCompletedBytes() int64 {
	var completedBytes int64
	for i, part := range cp.Parts {
		if cp.PartStat[i] {
			completedBytes += (part.End - part.Start + 1)
		}
	}
	return completedBytes
}

// initialize the multipart upload
func (cp *copyCheckpoint) prepare(srcBucket *Bucket, srcObjectKey string, destBucket *Bucket, destObjectKey string,
	partSize int64, options []Option) error {
	// cp
	cp.Magic = copyCpMagic
	cp.SrcBucketName = srcBucket.BucketName
	cp.SrcObjectKey = srcObjectKey
	cp.DestBucketName = destBucket.BucketName
	cp.DestObjectKey = destObjectKey

	// object
	meta, err := srcBucket.GetObjectDetailedMeta(srcObjectKey)
	if err != nil {
		return err
	}

	objectSize, err := strconv.ParseInt(meta.Get(HTTPHeaderContentLength), 10, 0)
	if err != nil {
		return err
	}

	cp.ObjStat.Size = objectSize
	cp.ObjStat.LastModified = meta.Get(HTTPHeaderLastModified)
	cp.ObjStat.Etag = meta.Get(HTTPHeaderEtag)

	// parts
	cp.Parts, err = getCopyParts(srcBucket, srcObjectKey, partSize)
	if err != nil {
		return err
	}
	cp.PartStat = make([]bool, len(cp.Parts))
	for i := range cp.PartStat {
		cp.PartStat[i] = false
	}
	cp.CopyParts = make([]UploadPart, len(cp.Parts))

	// init copy
	imur, err := destBucket.InitiateMultipartUpload(destObjectKey, options...)
	if err != nil {
		return err
	}
	cp.CopyID = imur.UploadID

	return nil
}

func (cp *copyCheckpoint) complete(bucket *Bucket, parts []UploadPart, cpFilePath string) error {
	imur := InitiateMultipartUploadResult{Bucket: cp.DestBucketName,
		Key: cp.DestObjectKey, UploadID: cp.CopyID}
	_, err := bucket.CompleteMultipartUpload(imur, parts)
	if err != nil {
		return err
	}
	os.Remove(cpFilePath)
	return err
}

// concurrently copy with checkpoint
func (bucket Bucket) copyFileWithCp(srcBucketName, srcObjectKey, destBucketName, destObjectKey string,
	partSize int64, options []Option, cpFilePath string, routines int) error {
	descBucket, err := bucket.Client.Bucket(destBucketName)
	srcBucket, err := bucket.Client.Bucket(srcBucketName)
	listener := getProgressListener(options)

	// LOAD CP data
	ccp := copyCheckpoint{}
	err = ccp.load(cpFilePath)
	if err != nil {
		os.Remove(cpFilePath)
	}

	// LOAD error or the cp data is invalid---reinitialize
	valid, err := ccp.isValid(srcBucket, srcObjectKey)
	if err != nil || !valid {
		if err = ccp.prepare(srcBucket, srcObjectKey, descBucket, destObjectKey, partSize, options); err != nil {
			return err
		}
		os.Remove(cpFilePath)
	}

	// unfinished parts.
	parts := ccp.todoParts()
	imur := InitiateMultipartUploadResult{
		Bucket:   destBucketName,
		Key:      destObjectKey,
		UploadID: ccp.CopyID}

	jobs := make(chan copyPart, len(parts))
	results := make(chan UploadPart, len(parts))
	failed := make(chan error)
	die := make(chan bool)

	completedBytes := ccp.getCompletedBytes()
	event := newProgressEvent(TransferStartedEvent, completedBytes, ccp.ObjStat.Size)
	publishProgress(listener, event)

	// start the worker threads
	arg := copyWorkerArg{descBucket, imur, srcBucketName, srcObjectKey, options, copyPartHooker}
	for w := 1; w <= routines; w++ {
		go copyWorker(w, arg, jobs, results, failed, die)
	}

	// start the scheduler
	go copyScheduler(jobs, parts)

	// waits for the parts completed.
	completed := 0
	for completed < len(parts) {
		select {
		case part := <-results:
			completed++
			ccp.update(part)
			ccp.dump(cpFilePath)
			completedBytes += (parts[part.PartNumber-1].End - parts[part.PartNumber-1].Start + 1)
			event = newProgressEvent(TransferDataEvent, completedBytes, ccp.ObjStat.Size)
			publishProgress(listener, event)
		case err := <-failed:
			close(die)
			event = newProgressEvent(TransferFailedEvent, completedBytes, ccp.ObjStat.Size)
			publishProgress(listener, event)
			return err
		}

		if completed >= len(parts) {
			break
		}
	}

	event = newProgressEvent(TransferCompletedEvent, completedBytes, ccp.ObjStat.Size)
	publishProgress(listener, event)

	return ccp.complete(descBucket, ccp.CopyParts, cpFilePath)
}
