package sample

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// GetObjectSample demos the streaming download, range download and download with checkpoint
func GetObjectSample() {
	// creates Bucket
	bucket, err := GetTestBucket(bucketName)
	if err != nil {
		HandleError(err)
	}

	// uploads the object
	err = bucket.PutObjectFromFile(objectKey, localFile)
	if err != nil {
		HandleError(err)
	}

	// case 1：downloads the object into ReadCloser(). The body needs to be closed
	body, err := bucket.GetObject(objectKey)
	if err != nil {
		HandleError(err)
	}
	data, err := ioutil.ReadAll(body)
	body.Close()
	if err != nil {
		HandleError(err)
	}
	data = data // use data

	// case 2：downloads the object to byte array. This is for small object.
	buf := new(bytes.Buffer)
	body, err = bucket.GetObject(objectKey)
	if err != nil {
		HandleError(err)
	}
	io.Copy(buf, body)
	body.Close()

	// case3：downloads the object to local file. The file handle needs to be specified
	fd, err := os.OpenFile("mynewfile-1.jpg", os.O_WRONLY|os.O_CREATE, 0660)
	if err != nil {
		HandleError(err)
	}
	defer fd.Close()

	body, err = bucket.GetObject(objectKey)
	if err != nil {
		HandleError(err)
	}
	io.Copy(fd, body)
	body.Close()

	// case 4：downloads the object to local file with file name specified
	err = bucket.GetObjectToFile(objectKey, "mynewfile-2.jpg")
	if err != nil {
		HandleError(err)
	}

	// case 5：gets the object with contraints. When contraints are met, download the file. OTherwise return precondition error
	// last modified time constraint is met, download the file
	body, err = bucket.GetObject(objectKey, oss.IfModifiedSince(pastDate))
	if err != nil {
		HandleError(err)
	}
	body.Close()
	// last modified time contraint is not met, do not download the file
	_, err = bucket.GetObject(objectKey, oss.IfUnmodifiedSince(pastDate))
	if err == nil {
		HandleError(err)
	}

	meta, err := bucket.GetObjectDetailedMeta(objectKey)
	if err != nil {
		HandleError(err)
	}
	etag := meta.Get(oss.HTTPHeaderEtag)
	// Etag contraint is met, download the file
	body, err = bucket.GetObject(objectKey, oss.IfMatch(etag))
	if err != nil {
		HandleError(err)
	}
	body.Close()

	// ETag contraint is not met, do not download the file
	body, err = bucket.GetObject(objectKey, oss.IfNoneMatch(etag))
	if err == nil {
		HandleError(err)
	}

	// case 6：big file's multipart download, concurrent and checkpoint is supported.
	// multipart download with part size 100KB. By default single thread is used and no checkpoint
	err = bucket.DownloadFile(objectKey, "mynewfile-3.jpg", 100*1024)
	if err != nil {
		HandleError(err)
	}

	// part size is 100K and thee threads are used
	err = bucket.DownloadFile(objectKey, "mynewfile-3.jpg", 100*1024, oss.Routines(3))
	if err != nil {
		HandleError(err)
	}

	// part size is 100K and three threads with checkpoint are used.
	err = bucket.DownloadFile(objectKey, "mynewfile-3.jpg", 100*1024, oss.Routines(3), oss.Checkpoint(true, ""))
	if err != nil {
		HandleError(err)
	}

	// specify the checkpoint file path
	err = bucket.DownloadFile(objectKey, "mynewfile-3.jpg", 100*1024, oss.Checkpoint(true, "mynewfile.cp"))
	if err != nil {
		HandleError(err)
	}

	// case 7：Use GZIP encoding for downloading the file.
	err = bucket.PutObjectFromFile(objectKey, htmlLocalFile)
	if err != nil {
		HandleError(err)
	}

	err = bucket.GetObjectToFile(objectKey, "myhtml.gzip", oss.AcceptEncoding("gzip"))
	if err != nil {
		HandleError(err)
	}

	// deletes the object and bucket
	err = DeleteTestBucketAndObject(bucketName)
	if err != nil {
		HandleError(err)
	}

	fmt.Println("GetObjectSample completed")
}
