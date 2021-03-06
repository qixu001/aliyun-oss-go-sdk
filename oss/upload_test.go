package oss

import (
	"fmt"
	"io"
	"os"
	"time"

	. "gopkg.in/check.v1"
)

type OssUploadSuite struct {
	client *Client
	bucket *Bucket
}

var _ = Suite(&OssUploadSuite{})

// Run once when the suite starts running
func (s *OssUploadSuite) SetUpSuite(c *C) {
	client, err := New(endpoint, accessID, accessKey)
	c.Assert(err, IsNil)
	s.client = client

	s.client.CreateBucket(bucketName)
	time.Sleep(5 * time.Second)

	bucket, err := s.client.Bucket(bucketName)
	c.Assert(err, IsNil)
	s.bucket = bucket

	testLogger.Println("test upload started")
}

// Run before each test or benchmark starts running
func (s *OssUploadSuite) TearDownSuite(c *C) {
	// Delete Part
	lmur, err := s.bucket.ListMultipartUploads()
	c.Assert(err, IsNil)

	for _, upload := range lmur.Uploads {
		var imur = InitiateMultipartUploadResult{Bucket: s.bucket.BucketName,
			Key: upload.Key, UploadID: upload.UploadID}
		err = s.bucket.AbortMultipartUpload(imur)
		c.Assert(err, IsNil)
	}

	// Delete Objects
	lor, err := s.bucket.ListObjects()
	c.Assert(err, IsNil)

	for _, object := range lor.Objects {
		err = s.bucket.DeleteObject(object.Key)
		c.Assert(err, IsNil)
	}

	testLogger.Println("test upload completed")
}

// Run after each test or benchmark runs
func (s *OssUploadSuite) SetUpTest(c *C) {
	err := removeTempFiles("../oss", ".jpg")
	c.Assert(err, IsNil)
}

// Run once after all tests or benchmarks have finished running
func (s *OssUploadSuite) TearDownTest(c *C) {
	err := removeTempFiles("../oss", ".jpg")
	c.Assert(err, IsNil)
}

// TestUploadRoutineWithoutRecovery test multithreaded upload without checkpoint
func (s *OssUploadSuite) TestUploadRoutineWithoutRecovery(c *C) {
	objectName := objectNamePrefix + "turwr"
	fileName := "../sample/BingWallpaper-2015-11-07.jpg"
	newFile := "upload-new-file.jpg"

	// Routines is not specified, by default single thread
	err := s.bucket.UploadFile(objectName, fileName, 100*1024)
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err := compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// specifies thread count as 1
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(1))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// specifies thread count as 3, which is smaller than parts count 5
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(3))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// specifies thread count as 5, which is same as the part count 5
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(5))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// specifies thread count as 10, which is bigger than the part count 5.
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(10))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// invalid thread count, it will use 1 automatically.
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(0))
	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// invalid thread count, it will use 1 automatically
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(-1))
	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// option
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(3), Meta("myprop", "mypropval"))

	meta, err := s.bucket.GetObjectDetailedMeta(objectName)
	c.Assert(err, IsNil)
	c.Assert(meta.Get("X-Oss-Meta-Myprop"), Equals, "mypropval")

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)
}

// ErrorHooker UploadPart Hook---it will fail the 5th part's upload.
func ErrorHooker(id int, chunk FileChunk) error {
	if chunk.Number == 5 {
		time.Sleep(time.Second)
		return fmt.Errorf("ErrorHooker")
	}
	return nil
}

// TestUploadRoutineWithoutRecovery multithreaded upload without checkpoint
func (s *OssUploadSuite) TestUploadRoutineWithoutRecoveryNegative(c *C) {
	objectName := objectNamePrefix + "turwrn"
	fileName := "../sample/BingWallpaper-2015-11-07.jpg"

	uploadPartHooker = ErrorHooker
	// worker thread error
	err := s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(2))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "ErrorHooker")
	uploadPartHooker = defaultUploadPart

	// local file does not exist
	err = s.bucket.UploadFile(objectName, "NotExist", 100*1024, Routines(2))
	c.Assert(err, NotNil)

	// the part size is invalid
	err = s.bucket.UploadFile(objectName, fileName, 1024, Routines(2))
	c.Assert(err, NotNil)

	err = s.bucket.UploadFile(objectName, fileName, 1024*1024*1024*100, Routines(2))
	c.Assert(err, NotNil)
}

// TestUploadRoutineWithRecovery multithreaded upload with checkpoint
func (s *OssUploadSuite) TestUploadRoutineWithRecovery(c *C) {
	objectName := objectNamePrefix + "turtr"
	fileName := "../sample/BingWallpaper-2015-11-07.jpg"
	newFile := "upload-new-file-2.jpg"

	// use default Routines and default CP file path (fileName+.cp)
	// first upload for 4 parts
	uploadPartHooker = ErrorHooker
	err := s.bucket.UploadFile(objectName, fileName, 100*1024, Checkpoint(true, ""))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "ErrorHooker")
	uploadPartHooker = defaultUploadPart

	// check cp
	ucp := uploadCheckpoint{}
	err = ucp.load(fileName + ".cp")
	c.Assert(err, IsNil)
	c.Assert(ucp.Magic, Equals, uploadCpMagic)
	c.Assert(len(ucp.MD5), Equals, len("LC34jZU5xK4hlxi3Qn3XGQ=="))
	c.Assert(ucp.FilePath, Equals, fileName)
	c.Assert(ucp.FileStat.Size, Equals, int64(482048))
	c.Assert(len(ucp.FileStat.LastModified.String()) > 0, Equals, true)
	c.Assert(ucp.FileStat.MD5, Equals, "")
	c.Assert(ucp.ObjectKey, Equals, objectName)
	c.Assert(len(ucp.UploadID), Equals, len("3F79722737D1469980DACEDCA325BB52"))
	c.Assert(len(ucp.Parts), Equals, 5)
	c.Assert(len(ucp.todoParts()), Equals, 1)
	c.Assert(len(ucp.allParts()), Equals, 5)

	// second upload, finish the remaining part
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Checkpoint(true, ""))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err := compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	err = ucp.load(fileName + ".cp")
	c.Assert(err, NotNil)

	// specifies Routines and CP
	uploadPartHooker = ErrorHooker
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(2), Checkpoint(true, objectName+".cp"))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "ErrorHooker")
	uploadPartHooker = defaultUploadPart

	// check cp
	ucp = uploadCheckpoint{}
	err = ucp.load(objectName + ".cp")
	c.Assert(err, IsNil)
	c.Assert(ucp.Magic, Equals, uploadCpMagic)
	c.Assert(len(ucp.MD5), Equals, len("LC34jZU5xK4hlxi3Qn3XGQ=="))
	c.Assert(ucp.FilePath, Equals, fileName)
	c.Assert(ucp.FileStat.Size, Equals, int64(482048))
	c.Assert(len(ucp.FileStat.LastModified.String()) > 0, Equals, true)
	c.Assert(ucp.FileStat.MD5, Equals, "")
	c.Assert(ucp.ObjectKey, Equals, objectName)
	c.Assert(len(ucp.UploadID), Equals, len("3F79722737D1469980DACEDCA325BB52"))
	c.Assert(len(ucp.Parts), Equals, 5)
	c.Assert(len(ucp.todoParts()), Equals, 1)
	c.Assert(len(ucp.allParts()), Equals, 5)

	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(3), Checkpoint(true, objectName+".cp"))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	err = ucp.load(objectName + ".cp")
	c.Assert(err, NotNil)

	// uploads all 5 parts without error
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(3), Checkpoint(true, ""))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// upload all 5 parts with 10 threads without error
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(10), Checkpoint(true, ""))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)

	// option
	err = s.bucket.UploadFile(objectName, fileName, 100*1024, Routines(3), Checkpoint(true, ""), Meta("myprop", "mypropval"))

	meta, err := s.bucket.GetObjectDetailedMeta(objectName)
	c.Assert(err, IsNil)
	c.Assert(meta.Get("X-Oss-Meta-Myprop"), Equals, "mypropval")

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err = compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)
}

// TestUploadRoutineWithoutRecovery multithreaded upload without checkpoint
func (s *OssUploadSuite) TestUploadRoutineWithRecoveryNegative(c *C) {
	objectName := objectNamePrefix + "turrn"
	fileName := "../sample/BingWallpaper-2015-11-07.jpg"

	// the local file does not exist
	err := s.bucket.UploadFile(objectName, "NotExist", 100*1024, Checkpoint(true, ""))
	c.Assert(err, NotNil)

	err = s.bucket.UploadFile(objectName, "NotExist", 100*1024, Routines(2), Checkpoint(true, ""))
	c.Assert(err, NotNil)

	// specified part size is invalid
	err = s.bucket.UploadFile(objectName, fileName, 1024, Checkpoint(true, ""))
	c.Assert(err, NotNil)

	err = s.bucket.UploadFile(objectName, fileName, 1024, Routines(2), Checkpoint(true, ""))
	c.Assert(err, NotNil)

	err = s.bucket.UploadFile(objectName, fileName, 1024*1024*1024*100, Checkpoint(true, ""))
	c.Assert(err, NotNil)

	err = s.bucket.UploadFile(objectName, fileName, 1024*1024*1024*100, Routines(2), Checkpoint(true, ""))
	c.Assert(err, NotNil)
}

// TestUploadLocalFileChange the file is updated while being uploaded
func (s *OssUploadSuite) TestUploadLocalFileChange(c *C) {
	objectName := objectNamePrefix + "tulfc"
	fileName := "../sample/BingWallpaper-2015-11-07.jpg"
	localFile := "BingWallpaper-2015-11-07.jpg"
	newFile := "upload-new-file-3.jpg"

	os.Remove(localFile)
	err := copyFile(fileName, localFile)
	c.Assert(err, IsNil)

	// first upload for 4 parts
	uploadPartHooker = ErrorHooker
	err = s.bucket.UploadFile(objectName, localFile, 100*1024, Checkpoint(true, ""))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "ErrorHooker")
	uploadPartHooker = defaultUploadPart

	os.Remove(localFile)
	err = copyFile(fileName, localFile)
	c.Assert(err, IsNil)

	// updating the file. The second upload will re-upload all 5 parts
	err = s.bucket.UploadFile(objectName, localFile, 100*1024, Checkpoint(true, ""))
	c.Assert(err, IsNil)

	os.Remove(newFile)
	err = s.bucket.GetObjectToFile(objectName, newFile)
	c.Assert(err, IsNil)

	eq, err := compareFiles(fileName, newFile)
	c.Assert(err, IsNil)
	c.Assert(eq, Equals, true)

	err = s.bucket.DeleteObject(objectName)
	c.Assert(err, IsNil)
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
