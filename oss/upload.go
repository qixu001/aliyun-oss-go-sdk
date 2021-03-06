package oss

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"time"
)

//
// UploadFile multipart file upload
//
// objectKey  object name
// filePath   local file path to upload
// partSize   the part size in byte
// options    the options for uploading object.
//
// error it will be nil if the operation succeeds; otherwise it's the error object.
//
func (bucket Bucket) UploadFile(objectKey, filePath string, partSize int64, options ...Option) error {
	if partSize < MinPartSize || partSize > MaxPartSize {
		return errors.New("oss: part size invalid range (1024KB, 5GB]")
	}

	cpConf, err := getCpConfig(options, filePath)
	if err != nil {
		return err
	}

	routines := getRoutines(options)

	if cpConf.IsEnable {
		return bucket.uploadFileWithCp(objectKey, filePath, partSize, options, cpConf.FilePath, routines)
	}

	return bucket.uploadFile(objectKey, filePath, partSize, options, routines)
}

// ----- concurrent upload without checkpoint  -----

// gets Checkpoint configuration
func getCpConfig(options []Option, filePath string) (*cpConfig, error) {
	cpc := &cpConfig{}
	cpcOpt, err := findOption(options, checkpointConfig, nil)
	if err != nil || cpcOpt == nil {
		return cpc, err
	}

	cpc = cpcOpt.(*cpConfig)
	if cpc.IsEnable && cpc.FilePath == "" {
		cpc.FilePath = filePath + CheckpointFileSuffix
	}

	return cpc, nil
}

// gets the thread count. by default it's 1.
func getRoutines(options []Option) int {
	rtnOpt, err := findOption(options, routineNum, nil)
	if err != nil || rtnOpt == nil {
		return 1
	}

	rs := rtnOpt.(int)
	if rs < 1 {
		rs = 1
	} else if rs > 100 {
		rs = 100
	}

	return rs
}

// gets the progress callback
func getProgressListener(options []Option) ProgressListener {
	isSet, listener, _ := isOptionSet(options, progressListener)
	if !isSet {
		return nil
	}
	return listener.(ProgressListener)
}

// test purpose hook
type uploadPartHook func(id int, chunk FileChunk) error

var uploadPartHooker uploadPartHook = defaultUploadPart

func defaultUploadPart(id int, chunk FileChunk) error {
	return nil
}

// worker argument structure
type workerArg struct {
	bucket   *Bucket
	filePath string
	imur     InitiateMultipartUploadResult
	hook     uploadPartHook
}

// worker thread function
func worker(id int, arg workerArg, jobs <-chan FileChunk, results chan<- UploadPart, failed chan<- error, die <-chan bool) {
	for chunk := range jobs {
		if err := arg.hook(id, chunk); err != nil {
			failed <- err
			break
		}
		part, err := arg.bucket.UploadPartFromFile(arg.imur, arg.filePath, chunk.Offset, chunk.Size, chunk.Number)
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

// scheduler function
func scheduler(jobs chan FileChunk, chunks []FileChunk) {
	for _, chunk := range chunks {
		jobs <- chunk
	}
	close(jobs)
}

func getTotalBytes(chunks []FileChunk) int64 {
	var tb int64
	for _, chunk := range chunks {
		tb += chunk.Size
	}
	return tb
}

// concurrent upload, without checkpoint
func (bucket Bucket) uploadFile(objectKey, filePath string, partSize int64, options []Option, routines int) error {
	listener := getProgressListener(options)

	chunks, err := SplitFileByPartSize(filePath, partSize)
	if err != nil {
		return err
	}

	// initialize the multipart upload
	imur, err := bucket.InitiateMultipartUpload(objectKey, options...)
	if err != nil {
		return err
	}

	jobs := make(chan FileChunk, len(chunks))
	results := make(chan UploadPart, len(chunks))
	failed := make(chan error)
	die := make(chan bool)

	var completedBytes int64
	totalBytes := getTotalBytes(chunks)
	event := newProgressEvent(TransferStartedEvent, 0, totalBytes)
	publishProgress(listener, event)

	// starts the worker thread
	arg := workerArg{&bucket, filePath, imur, uploadPartHooker}
	for w := 1; w <= routines; w++ {
		go worker(w, arg, jobs, results, failed, die)
	}

	// schedule the jobs
	go scheduler(jobs, chunks)

	// waiting for the upload finished
	completed := 0
	parts := make([]UploadPart, len(chunks))
	for completed < len(chunks) {
		select {
		case part := <-results:
			completed++
			parts[part.PartNumber-1] = part
			completedBytes += chunks[part.PartNumber-1].Size
			event = newProgressEvent(TransferDataEvent, completedBytes, totalBytes)
			publishProgress(listener, event)
		case err := <-failed:
			close(die)
			event = newProgressEvent(TransferFailedEvent, completedBytes, totalBytes)
			publishProgress(listener, event)
			bucket.AbortMultipartUpload(imur)
			return err
		}

		if completed >= len(chunks) {
			break
		}
	}

	event = newProgressEvent(TransferStartedEvent, completedBytes, totalBytes)
	publishProgress(listener, event)

	// complete the multpart upload
	_, err = bucket.CompleteMultipartUpload(imur, parts)
	if err != nil {
		bucket.AbortMultipartUpload(imur)
		return err
	}
	return nil
}

// ----- concurrent upload with checkpoint  -----
const uploadCpMagic = "FE8BB4EA-B593-4FAC-AD7A-2459A36E2E62"

type uploadCheckpoint struct {
	Magic     string   // magic
	MD5       string   // checkpoint file content's MD5
	FilePath  string   // local file path
	FileStat  cpStat   // file state
	ObjectKey string   // key
	UploadID  string   // upload id
	Parts     []cpPart // all parts of the local file
}

type cpStat struct {
	Size         int64     // file size
	LastModified time.Time // file's last modified time
	MD5          string    // local file's MD5
}

type cpPart struct {
	Chunk       FileChunk  // file chunk
	Part        UploadPart // uploaded part
	IsCompleted bool       // upload complete flag
}

// check if the uploaded data is valid---it's valid when the file is not updated and the checkpoint data is valid.
func (cp uploadCheckpoint) isValid(filePath string) (bool, error) {
	// compares the CP's magic number and MD5.
	cpb := cp
	cpb.MD5 = ""
	js, _ := json.Marshal(cpb)
	sum := md5.Sum(js)
	b64 := base64.StdEncoding.EncodeToString(sum[:])

	if cp.Magic != uploadCpMagic || b64 != cp.MD5 {
		return false, nil
	}

	// makes sure if the local file is updated.
	fd, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer fd.Close()

	st, err := fd.Stat()
	if err != nil {
		return false, err
	}

	md, err := calcFileMD5(filePath)
	if err != nil {
		return false, err
	}

	// compares the file size, file's last modified time and file's MD5
	if cp.FileStat.Size != st.Size() ||
		cp.FileStat.LastModified != st.ModTime() ||
		cp.FileStat.MD5 != md {
		return false, nil
	}

	return true, nil
}

// load from the file
func (cp *uploadCheckpoint) load(filePath string) error {
	contents, err := ioutil.ReadFile(filePath)
	if err != nil {
		return err
	}

	err = json.Unmarshal(contents, cp)
	return err
}

// dump to the local file
func (cp *uploadCheckpoint) dump(filePath string) error {
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

	// dump
	return ioutil.WriteFile(filePath, js, FilePermMode)
}

// updates the part status
func (cp *uploadCheckpoint) updatePart(part UploadPart) {
	cp.Parts[part.PartNumber-1].Part = part
	cp.Parts[part.PartNumber-1].IsCompleted = true
}

// unfinished parts
func (cp *uploadCheckpoint) todoParts() []FileChunk {
	fcs := []FileChunk{}
	for _, part := range cp.Parts {
		if !part.IsCompleted {
			fcs = append(fcs, part.Chunk)
		}
	}
	return fcs
}

// all parts
func (cp *uploadCheckpoint) allParts() []UploadPart {
	ps := []UploadPart{}
	for _, part := range cp.Parts {
		ps = append(ps, part.Part)
	}
	return ps
}

// completed bytes count
func (cp *uploadCheckpoint) getCompletedBytes() int64 {
	var completedBytes int64
	for _, part := range cp.Parts {
		if part.IsCompleted {
			completedBytes += part.Chunk.Size
		}
	}
	return completedBytes
}

// calculates the MD5 for the specified local file
func calcFileMD5(filePath string) (string, error) {
	return "", nil
}

// initialize the multipart upload
func prepare(cp *uploadCheckpoint, objectKey, filePath string, partSize int64, bucket *Bucket, options []Option) error {
	// cp
	cp.Magic = uploadCpMagic
	cp.FilePath = filePath
	cp.ObjectKey = objectKey

	// localfile
	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()

	st, err := fd.Stat()
	if err != nil {
		return err
	}
	cp.FileStat.Size = st.Size()
	cp.FileStat.LastModified = st.ModTime()
	md, err := calcFileMD5(filePath)
	if err != nil {
		return err
	}
	cp.FileStat.MD5 = md

	// chunks
	parts, err := SplitFileByPartSize(filePath, partSize)
	if err != nil {
		return err
	}

	cp.Parts = make([]cpPart, len(parts))
	for i, part := range parts {
		cp.Parts[i].Chunk = part
		cp.Parts[i].IsCompleted = false
	}

	// init load
	imur, err := bucket.InitiateMultipartUpload(objectKey, options...)
	if err != nil {
		return err
	}
	cp.UploadID = imur.UploadID

	return nil
}

// completes the multipart upload and deletes the local CP files
func complete(cp *uploadCheckpoint, bucket *Bucket, parts []UploadPart, cpFilePath string) error {
	imur := InitiateMultipartUploadResult{Bucket: bucket.BucketName,
		Key: cp.ObjectKey, UploadID: cp.UploadID}
	_, err := bucket.CompleteMultipartUpload(imur, parts)
	if err != nil {
		return err
	}
	os.Remove(cpFilePath)
	return err
}

// concurrent upload with checkpoint
func (bucket Bucket) uploadFileWithCp(objectKey, filePath string, partSize int64, options []Option, cpFilePath string, routines int) error {
	listener := getProgressListener(options)

	// LOAD CP data
	ucp := uploadCheckpoint{}
	err := ucp.load(cpFilePath)
	if err != nil {
		os.Remove(cpFilePath)
	}

	// LOAD error or the cp data is invalid.
	valid, err := ucp.isValid(filePath)
	if err != nil || !valid {
		if err = prepare(&ucp, objectKey, filePath, partSize, &bucket, options); err != nil {
			return err
		}
		os.Remove(cpFilePath)
	}

	chunks := ucp.todoParts()
	imur := InitiateMultipartUploadResult{
		Bucket:   bucket.BucketName,
		Key:      objectKey,
		UploadID: ucp.UploadID}

	jobs := make(chan FileChunk, len(chunks))
	results := make(chan UploadPart, len(chunks))
	failed := make(chan error)
	die := make(chan bool)

	completedBytes := ucp.getCompletedBytes()
	event := newProgressEvent(TransferStartedEvent, completedBytes, ucp.FileStat.Size)
	publishProgress(listener, event)

	// starts the workers
	arg := workerArg{&bucket, filePath, imur, uploadPartHooker}
	for w := 1; w <= routines; w++ {
		go worker(w, arg, jobs, results, failed, die)
	}

	// schedule jobs
	go scheduler(jobs, chunks)

	// waiting for the job finished
	completed := 0
	for completed < len(chunks) {
		select {
		case part := <-results:
			completed++
			ucp.updatePart(part)
			ucp.dump(cpFilePath)
			completedBytes += ucp.Parts[part.PartNumber-1].Chunk.Size
			event = newProgressEvent(TransferDataEvent, completedBytes, ucp.FileStat.Size)
			publishProgress(listener, event)
		case err := <-failed:
			close(die)
			event = newProgressEvent(TransferFailedEvent, completedBytes, ucp.FileStat.Size)
			publishProgress(listener, event)
			return err
		}

		if completed >= len(chunks) {
			break
		}
	}

	event = newProgressEvent(TransferCompletedEvent, completedBytes, ucp.FileStat.Size)
	publishProgress(listener, event)

	// complete the multipart upload
	err = complete(&ucp, &bucket, ucp.allParts(), cpFilePath)
	return err
}
