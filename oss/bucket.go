package oss

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"hash"
	"hash/crc64"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Bucket implements the operations of object.
type Bucket struct {
	Client     Client
	BucketName string
}

//
// PutObject Creates a new object and it will overwrite the original one if it exists already.
//
// objectKey  The object key in UTF-8 encoding. The length must be between 1 to 1023 and cannot start with "/" or "\".
// reader     io.Reader instance for reading the data for uploading
// options    The options for uploading the object. The valid options here are CacheControl, ContentDisposition, ContentEncoding
// Expires,ServerSideEncryption, ObjectACL and Meta. Please checks out the following link for the detail.
// https://help.aliyun.com/document_detail/oss/api-reference/object/PutObject.html
//
// error  it will be nil if the operation succeeds, non-null if errors occurred.
//
func (bucket Bucket) PutObject(objectKey string, reader io.Reader, options ...Option) error {
	opts := addContentType(options, objectKey)

	request := &PutObjectRequest{
		ObjectKey: objectKey,
		Reader:    reader,
	}
	resp, err := bucket.DoPutObject(request, opts)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return err
}

//
// PutObjectFromFile Creates a new object from the local file.
//
// objectKey object key
// filePath  The local file path to upload.
// options   The options for uploading the object. Checks out the details in parameter options in PutObject.
//
// error  It returns nil if no error, otherwise return the error object.
//
func (bucket Bucket) PutObjectFromFile(objectKey, filePath string, options ...Option) error {
	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()

	opts := addContentType(options, filePath, objectKey)

	request := &PutObjectRequest{
		ObjectKey: objectKey,
		Reader:    fd,
	}
	resp, err := bucket.DoPutObject(request, opts)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return err
}

//
// DoPutObject Does the actual upload work
//
// request  The request instance for uploading an object
// options  The options for uploading an object.
//
// Response The response from OSS
// error  It's nil if no errors, otherwise it's the error object.
//
func (bucket Bucket) DoPutObject(request *PutObjectRequest, options []Option) (*Response, error) {
	isOptSet, _, _ := isOptionSet(options, HTTPHeaderContentType)
	if !isOptSet {
		options = addContentType(options, request.ObjectKey)
	}

	listener := getProgressListener(options)

	params := map[string]interface{}{}
	resp, err := bucket.do("PUT", request.ObjectKey, params, options, request.Reader, listener)
	if err != nil {
		return nil, err
	}

	if bucket.getConfig().IsEnableCRC {
		err = checkCRC(resp, "DoPutObject")
		if err != nil {
			return resp, err
		}
	}

	err = checkRespCode(resp.StatusCode, []int{http.StatusOK})

	return resp, err
}

//
// GetObject Download the object.
//
// objectKey The object key.
// options   The options for downloading the object. The valid values are: Range, IfModifiedSince, IfUnmodifiedSince, IfMatch,
// IfNoneMatch,AcceptEncoding. For more details, please check out:
// https://help.aliyun.com/document_detail/oss/api-reference/object/GetObject.html
//
// io.ReadCloser  reader instance for reading data from response. It must be called close() after the usage and only valid when error is nil.
// error  It's nil when no error occurred. Otherwise it's the error object.
//
func (bucket Bucket) GetObject(objectKey string, options ...Option) (io.ReadCloser, error) {
	result, err := bucket.DoGetObject(&GetObjectRequest{objectKey}, options)
	if err != nil {
		return nil, err
	}
	return result.Response.Body, nil
}

//
// GetObjectToFile Download the data to a local file
//
// objectKey  The object key to download
// filePath   The local file to store the object data
// options    The options for downloading the object. Checks out the parameter options in method GetObject.
//
// error  It's nil if no error; Otherwise it's the error object.
//
func (bucket Bucket) GetObjectToFile(objectKey, filePath string, options ...Option) error {
	tempFilePath := filePath + TempFileSuffix

	// calls the api to actually download the object. Returns the result instance
	result, err := bucket.DoGetObject(&GetObjectRequest{objectKey}, options)
	if err != nil {
		return err
	}
	defer result.Response.Body.Close()

	// If the local file does not exist, create a new one. If it exists, overwrites it.
	fd, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, FilePermMode)
	if err != nil {
		return err
	}

	// copy the data to the local file path.
	_, err = io.Copy(fd, result.Response.Body)
	fd.Close()
	if err != nil {
		return err
	}

	// compares the CRC value
	hasRange, _, _ := isOptionSet(options, HTTPHeaderRange)
	if bucket.getConfig().IsEnableCRC && !hasRange {
		result.Response.ClientCRC = result.ClientCRC.Sum64()
		err = checkCRC(result.Response, "GetObjectToFile")
		if err != nil {
			os.Remove(tempFilePath)
			return err
		}
	}

	return os.Rename(tempFilePath, filePath)
}

//
// DoGetObject the actual API that gets the object. It's the internal function called by other public APIs
//
// request The request to download the object.
// options    The options for downloading the file. Checks out the parameter options in method GetObject.
//
// GetObjectResult The result instance of getting the object
// error  It's nil if no error; otherwise it's the error object
//
func (bucket Bucket) DoGetObject(request *GetObjectRequest, options []Option) (*GetObjectResult, error) {
	params := map[string]interface{}{}
	resp, err := bucket.do("GET", request.ObjectKey, params, options, nil, nil)
	if err != nil {
		return nil, err
	}

	result := &GetObjectResult{
		Response: resp,
	}

	// crc
	var crcCalc hash.Hash64
	hasRange, _, _ := isOptionSet(options, HTTPHeaderRange)
	if bucket.getConfig().IsEnableCRC && !hasRange {
		crcCalc = crc64.New(crcTable())
		result.ServerCRC = resp.ServerCRC
		result.ClientCRC = crcCalc
	}

	// progress
	listener := getProgressListener(options)

	contentLen, _ := strconv.ParseInt(resp.Headers.Get(HTTPHeaderContentLength), 10, 64)
	resp.Body = ioutil.NopCloser(TeeReader(resp.Body, crcCalc, contentLen, listener, nil))

	return result, nil
}

//
// CopyObject Copy the object inside the bucket.
//
// srcObjectKey  The source object to copy
// destObjectKey The target object to copy
// options  Options for copying an object. You can specify the conditions of copy. The valid conditions are CopySourceIfMatch,
// CopySourceIfNoneMatch,CopySourceIfModifiedSince,CopySourceIfUnmodifiedSince,MetadataDirective.
// Also you can specify the target object's attributes, such as CacheControl,ContentDisposition,ContentEncoding,Expires,
// ServerSideEncryption, ObjectACL, Meta. For more details, check out this link:
// https://help.aliyun.com/document_detail/oss/api-reference/object/CopyObject.html
//
// error It's nil if no error; otherwise it's the error object.
//
func (bucket Bucket) CopyObject(srcObjectKey, destObjectKey string, options ...Option) (CopyObjectResult, error) {
	var out CopyObjectResult
	options = append(options, CopySource(bucket.BucketName, url.QueryEscape(srcObjectKey)))
	params := map[string]interface{}{}
	resp, err := bucket.do("PUT", destObjectKey, params, options, nil, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	err = xmlUnmarshal(resp.Body, &out)
	return out, err
}

//
// CopyObjectTo Copy the object to another bucket
//
// srcObjectKey   Source object key. The source bucket is Bucket.BucketName.
// destBucketName  Target Bucket name.
// destObjectKey  Target Object name.
// options        Copy options, check out parameter options in function CopyObject for more details.
//
// error  it's nil if no error; otherwise it's the error object.
//
func (bucket Bucket) CopyObjectTo(destBucketName, destObjectKey, srcObjectKey string, options ...Option) (CopyObjectResult, error) {
	return bucket.copy(srcObjectKey, destBucketName, destObjectKey, options...)
}

//
// CopyObjectFrom Copy the object to another bucket
//
// srcBucketName  Source bucket name
// srcObjectKey   Source object name
// destObjectKey  Target object name. The target bucket name is bucket.BucketName.
// options        Copy options. Check out parameter options in function CopyObject.
//
// error  it's nil if no error; otherwise it's an error object
//
func (bucket Bucket) CopyObjectFrom(srcBucketName, srcObjectKey, destObjectKey string, options ...Option) (CopyObjectResult, error) {
	destBucketName := bucket.BucketName
	var out CopyObjectResult
	srcBucket, err := bucket.Client.Bucket(srcBucketName)
	if err != nil {
		return out, err
	}

	return srcBucket.copy(srcObjectKey, destBucketName, destObjectKey, options...)
}

func (bucket Bucket) copy(srcObjectKey, destBucketName, destObjectKey string, options ...Option) (CopyObjectResult, error) {
	var out CopyObjectResult
	options = append(options, CopySource(bucket.BucketName, url.QueryEscape(srcObjectKey)))
	headers := make(map[string]string)
	err := handleOptions(headers, options)
	if err != nil {
		return out, err
	}
	params := map[string]interface{}{}
	resp, err := bucket.Client.Conn.Do("PUT", destBucketName, destObjectKey, params, headers, nil, 0, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	err = xmlUnmarshal(resp.Body, &out)
	return out, err
}

//
// AppendObject Upload the data in the way of appending an existing or new object.
//
// AppendObject the parameter appendPosition specifies which postion (in the target object) to append. For the first append (to a non-existing file)
// ，the appendPosition should be 0. The appendPosition in the subsequent calls will be the current object length.
// For example, the first appendObject's appendPosition is 0 and it uploaded 65536 bytes data, then the second call's position is 65536.
// The response header x-oss-next-append-position after each successful request also specifies the next call's append position (so the caller don't need to maintain this information).
//
// objectKey  Tbe target object to append to.
// reader     io.Reader，the read instance for reading the data too append.
// appendPosition  object's append position.
// the options for the first appending, such as CacheControl,ContentDisposition, ContentEncoding,
// Expires, ServerSideEncryption, ObjectACL。
//
// int64 The next append position--it's valid when error is nil.
// error it's nil if no error; otherwise it's the error object.
//
func (bucket Bucket) AppendObject(objectKey string, reader io.Reader, appendPosition int64, options ...Option) (int64, error) {
	request := &AppendObjectRequest{
		ObjectKey: objectKey,
		Reader:    reader,
		Position:  appendPosition,
	}

	result, err := bucket.DoAppendObject(request, options)
	if err != nil {
		return appendPosition, err
	}

	return result.NextPosition, err
}

//
// DoAppendObject The actual API that does the object append.
//
// request The request object for appending object.
// options The options for appending object.
//
// AppendObjectResult The result object for appending object.
// error  It's nil if no errors; otherwise it's the error object.
//
func (bucket Bucket) DoAppendObject(request *AppendObjectRequest, options []Option) (*AppendObjectResult, error) {
	params := map[string]interface{}{}
	params["append"] = nil
	params["position"] = strconv.FormatInt(request.Position, 10)
	headers := make(map[string]string)

	opts := addContentType(options, request.ObjectKey)
	handleOptions(headers, opts)

	var initCRC uint64
	isCRCSet, initCRCOpt, _ := isOptionSet(options, initCRC64)
	if isCRCSet {
		initCRC = initCRCOpt.(uint64)
	}

	listener := getProgressListener(options)

	handleOptions(headers, opts)
	resp, err := bucket.Client.Conn.Do("POST", bucket.BucketName, request.ObjectKey, params, headers,
		request.Reader, initCRC, listener)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	nextPosition, _ := strconv.ParseInt(resp.Headers.Get(HTTPHeaderOssNextAppendPosition), 10, 64)
	result := &AppendObjectResult{
		NextPosition: nextPosition,
		CRC:          resp.ServerCRC,
	}

	if bucket.getConfig().IsEnableCRC && isCRCSet {
		err = checkCRC(resp, "AppendObject")
		if err != nil {
			return result, err
		}
	}

	return result, nil
}

//
// DeleteObject Deletes the object.
//
// objectKey The object key to delete.
//
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) DeleteObject(objectKey string) error {
	params := map[string]interface{}{}
	resp, err := bucket.do("DELETE", objectKey, params, nil, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkRespCode(resp.StatusCode, []int{http.StatusNoContent})
}

//
// DeleteObjects Delete multiple objects.
//
// objectKeys The object keys to delete.
// options The options for deleting objects.
//         Supported option is DeleteObjectsQuiet which means it will not return error even deletion failed (not recommended). By default it's not used.
//
// DeleteObjectsResult The result object.
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) DeleteObjects(objectKeys []string, options ...Option) (DeleteObjectsResult, error) {
	out := DeleteObjectsResult{}
	dxml := deleteXML{}
	for _, key := range objectKeys {
		dxml.Objects = append(dxml.Objects, DeleteObject{Key: key})
	}
	isQuiet, _ := findOption(options, deleteObjectsQuiet, false)
	dxml.Quiet = isQuiet.(bool)

	bs, err := xml.Marshal(dxml)
	if err != nil {
		return out, err
	}
	buffer := new(bytes.Buffer)
	buffer.Write(bs)

	contentType := http.DetectContentType(buffer.Bytes())
	options = append(options, ContentType(contentType))
	sum := md5.Sum(bs)
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	options = append(options, ContentMD5(b64))

	params := map[string]interface{}{}
	params["delete"] = nil
	params["encoding-type"] = "url"

	resp, err := bucket.do("POST", "", params, options, buffer, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	if !dxml.Quiet {
		if err = xmlUnmarshal(resp.Body, &out); err == nil {
			err = decodeDeleteObjectsResult(&out)
		}
	}
	return out, err
}

//
// IsObjectExist Checks if the object exists.
//
// bool  flag of object's existence (true:exists;false:non-exist) when error is nil.
//
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) IsObjectExist(objectKey string) (bool, error) {
	_, err := bucket.GetObjectMeta(objectKey)
	if err == nil {
		return true, nil
	}

	switch err.(type) {
	case ServiceError:
		if err.(ServiceError).StatusCode == 404 && err.(ServiceError).Code == "NoSuchKey" {
			return false, nil
		}
	}

	return false, err
}

//
// ListObjects Lists the objects under the current bucket.
//
// options  It contains all the filters for listing object.
//          It could specify a prefix filter on object keys,  the max keys count to return and the object key marker and the delimiter for grouping object names.
//          The key marker means the returned objects' key must be greater than it in lexicographic order.
//
// For example, if the bucket has 8 objects，my-object-1, my-object-11, my-object-2, my-object-21,
// my-object-22, my-object-3, my-object-31, my-object-32. If the prefix is my-object-2 (no other filters), then it returns
// my-object-2, my-object-21, my-object-22 three objects. If the marker is my-object-22 (no other filters), then it returns
// my-object-3, my-object-31, my-object-32 three objects. If the max keys is 5, then it returns 5 objects.
// The three filters could be used together to achieve filter and paging functionality.
// If the prefix is the folder name, then it could list all files under this folder (including the files under its subfolders).
// But if the delimiter is specified with '/', then it only returns that folder's files (no subfolder's files). The direct subfolders are in the commonPrefixes properties.
// For example, if the bucket has three objects fun/test.jpg, fun/movie/001.avi, fun/movie/007.avi. And if the prefix is "fun/", then it returns all three objects.
// But if the delimiter is '/', then only "fun/test.jpg" is returned as files and fun/movie/ is returned as common prefix.
//
// For common usage scenario, checks out sample/list_object.go.
//
// ListObjectsResponse  The return value after operation succeeds (only valid when error is nil).
//
func (bucket Bucket) ListObjects(options ...Option) (ListObjectsResult, error) {
	var out ListObjectsResult

	options = append(options, EncodingType("url"))
	params, err := getRawParams(options)
	if err != nil {
		return out, err
	}

	resp, err := bucket.do("GET", "", params, nil, nil, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	err = xmlUnmarshal(resp.Body, &out)
	if err != nil {
		return out, err
	}

	err = decodeListObjectsResult(&out)
	return out, err
}

//
// SetObjectMeta Sets the metadata of the Object.
//
// objectKey object
// options Options for setting the metadata. The valid options are CacheControl, ContentDisposition, ContentEncoding, Expires,
// ServerSideEncryption, and custom metadata.
//
// error It's nil if no errors;otherwise it's the error object.
//
func (bucket Bucket) SetObjectMeta(objectKey string, options ...Option) error {
	options = append(options, MetadataDirective(MetaReplace))
	_, err := bucket.CopyObject(objectKey, objectKey, options...)
	return err
}

//
// GetObjectDetailedMeta Gets the object's detailed metadata
//
// objectKey object key.
// objectPropertyConstraints The contraints of the object. Only when the object meet the requirements this method will return the metadata. Otherwise returns error. Valid options are IfModifiedSince, IfUnmodifiedSince,
// IfMatch, IfNoneMatch. For more details check out https://help.aliyun.com/document_detail/oss/api-reference/object/HeadObject.html
//
// http.Header  object meta when error is nil.
// error  It's nil if no errors; otherwise it's the error object.
//
func (bucket Bucket) GetObjectDetailedMeta(objectKey string, options ...Option) (http.Header, error) {
	params := map[string]interface{}{}
	resp, err := bucket.do("HEAD", objectKey, params, options, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return resp.Headers, nil
}

//
// GetObjectMeta Gets object metadata.
//
// GetObjectMeta is more lightweight than GetObjectDetailedMeta as it only returns basic metadata including ETag
// size, LastModified. The size information is in the HTTP header Content-Length.
//
// objectKey object key
//
// http.Header the object's metadata, valid when error is nil.
// error it's nil if no error; otherwise it's the error object.
//
func (bucket Bucket) GetObjectMeta(objectKey string) (http.Header, error) {
	params := map[string]interface{}{}
	params["objectMeta"] = nil
	//resp, err := bucket.do("GET", objectKey, "?objectMeta", "", nil, nil, nil)
	resp, err := bucket.do("GET", objectKey, params, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return resp.Headers, nil
}

//
// SetObjectACL updates the object's ACL.
//
// Only the bucket's owner could update object's ACL and its priority is higher than bucket's ACL.
// For example, if the bucket ACL is private and object's ACL is public-read-write.
// Then object's ACL is used and it means all users could read or write that object.
// When the object's ACL is not set, then bucket's ACL is used as the object's ACL.
//
// Object read operations include GetObject, HeadObject, CopyObject and UploadPartCopy on the source object;
// Object write operations include PutObject，PostObject，AppendObject，DeleteObject DeleteMultipleObjects，
// CompleteMultipartUpload and CopyObject on target object.
//
// objectKey the target object key (to set the ACL on)
// objectAcl object ACL. Valid options are PrivateACL , PublicReadACL, PublicReadWriteACL.
//
// error it's nil if no error; otherwise it's the error object.
//
func (bucket Bucket) SetObjectACL(objectKey string, objectACL ACLType) error {
	options := []Option{ObjectACL(objectACL)}
	params := map[string]interface{}{}
	params["acl"] = nil
	resp, err := bucket.do("PUT", objectKey, params, options, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkRespCode(resp.StatusCode, []int{http.StatusOK})
}

//
// GetObjectACL Gets object's ACL
//
// objectKey the object to get ACL from.
//
// GetObjectACLResult The result object when error is nil.GetObjectACLResult.Acl is the object acl.
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) GetObjectACL(objectKey string) (GetObjectACLResult, error) {
	var out GetObjectACLResult
	params := map[string]interface{}{}
	params["acl"] = nil
	resp, err := bucket.do("GET", objectKey, params, nil, nil, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	err = xmlUnmarshal(resp.Body, &out)
	return out, err
}

//
// PutSymlink Creates a symlink (to point to an existing object)
//
// Symlink cannot point to another symlink.
// When creating a symlink, it does not check the existence of the target file, and does not check if the target file is symlink.
// Neither it checks the caller's permission on the target file. All these checks are deferred to the actual GetObject call via this symlink.
// If trying to add an existing file, as long as the caller has the write permission, the existing one will be overwritten.
// If the x-oss-meta- is specified, it will be added as the metadata of the symlink file.
//
// symObjectKey The symlink object's key
// targetObjectKey The target object key to point to.
//
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) PutSymlink(symObjectKey string, targetObjectKey string, options ...Option) error {
	options = append(options, symlinkTarget(url.QueryEscape(targetObjectKey)))
	params := map[string]interface{}{}
	params["symlink"] = nil
	resp, err := bucket.do("PUT", symObjectKey, params, options, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkRespCode(resp.StatusCode, []int{http.StatusOK})
}

//
// GetSymlink Gets the symlink object with the specified key.
// If the symlink object does not exist, returns 404.
//
// objectKey The symlink object's key.
//
// error it's nil if no error; otherwise it's the error object.
// When error is nil, the target file key is in the X-Oss-Symlink-Target header of the returned object.
//
func (bucket Bucket) GetSymlink(objectKey string) (http.Header, error) {
	params := map[string]interface{}{}
	params["symlink"] = nil
	resp, err := bucket.do("GET", objectKey, params, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	targetObjectKey := resp.Headers.Get(HTTPHeaderOssSymlinkTarget)
	targetObjectKey, err = url.QueryUnescape(targetObjectKey)
	if err != nil {
		return resp.Headers, err
	}
	resp.Headers.Set(HTTPHeaderOssSymlinkTarget, targetObjectKey)
	return resp.Headers, err
}

//
// RestoreObject Restore the object from the archive storage.
//
// An archive object is in cold status by default and it cannot be accessed.
// When restore is called on the cold object, it will become available for access after some time.
// If multiple restores are called on the same file when the object is being restored, server side does nothing for additional calls but returns success.
// By default, the restored object is available for access for one day. After that it will be unavailable again.
// But if another restored are called after the file is restored， then it will extend one day's access time of that object, up to 7 days.
//
// objectKey object key to restore.
//
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) RestoreObject(objectKey string) error {
	params := map[string]interface{}{}
	params["restore"] = nil
	resp, err := bucket.do("POST", objectKey, params, nil, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkRespCode(resp.StatusCode, []int{http.StatusOK, http.StatusAccepted})
}

//
// SignURL Sign the url. Users could access the object directly with this url without getting the AK.
//
// objectKey the target object to sign.
// signURLConfig The config for the signed url
//
// Returns the signed url, when error is nil.
// error it's nil if no error; otherwise it's the error object
//
func (bucket Bucket) SignURL(objectKey string, method HTTPMethod, expiredInSec int64, options ...Option) (string, error) {
	if expiredInSec < 0 {
		return "", fmt.Errorf("invalid expires: %d, expires must bigger than 0", expiredInSec)
	}
	expiration := time.Now().Unix() + expiredInSec

	params, err := getRawParams(options)
	if err != nil {
		return "", err
	}

	headers := make(map[string]string)
	err = handleOptions(headers, options)
	if err != nil {
		return "", err
	}

	return bucket.Client.Conn.signURL(method, bucket.BucketName, objectKey, expiration, params, headers), nil
}

//
// PutObjectWithURL Upload an object with the url. If the object exists, it will be overwritten.
// PutObjectWithURL It will not generate minetype according to the key name.
//
// signedURL  Signed url
// reader     io.Reader the read instance for reading the data for the upload.
// options    The options for uploading the data. The valid options are CacheControl, ContentDisposition, ContentEncoding,
// Expires, ServerSideEncryption, ObjectACL and custom metadata. Check out the following link for details:
// https://help.aliyun.com/document_detail/oss/api-reference/object/PutObject.html
//
// error  It's nil if no errors; otherwise it's the error object.
//
func (bucket Bucket) PutObjectWithURL(signedURL string, reader io.Reader, options ...Option) error {
	resp, err := bucket.DoPutObjectWithURL(signedURL, reader, options)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return err
}

//
// PutObjectFromFileWithURL Upload an object from a local file with the signed url.
// PutObjectFromFileWithURL It does not generate mimetype according to object key's name or the local file name.
//
// signedURL  signed URL.
// filePath  local file path, such as dirfile.txt, for uploading.
// options   Options for uploading, same as the options in PutObject function.
//
// error  It's nil if no error; otherwise it's an error object.
//
func (bucket Bucket) PutObjectFromFileWithURL(signedURL, filePath string, options ...Option) error {
	fd, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fd.Close()

	resp, err := bucket.DoPutObjectWithURL(signedURL, fd, options)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return err
}

//
// DoPutObjectWithURL The actual API that does the upload with URL work(internal for SDK)
//
// signedURL  signed URL.
// reader     io.Reader the read instance for getting the data to upload.
// options  Options for uploading.
//
// Response The response object which contains the HTTP response.
// error  It's nil if no errors; otherwise it's an error object.
//
func (bucket Bucket) DoPutObjectWithURL(signedURL string, reader io.Reader, options []Option) (*Response, error) {
	listener := getProgressListener(options)

	params := map[string]interface{}{}
	resp, err := bucket.doURL("PUT", signedURL, params, options, reader, listener)
	if err != nil {
		return nil, err
	}

	if bucket.getConfig().IsEnableCRC {
		err = checkCRC(resp, "DoPutObjectWithURL")
		if err != nil {
			return resp, err
		}
	}

	err = checkRespCode(resp.StatusCode, []int{http.StatusOK})

	return resp, err
}

//
// GetObjectWithURL Downloading the object and return the reader instance,  with the signed url.
//
// signedURL  Signed url.
// options   Options for downloading the object. Valid options are IfModifiedSince, IfUnmodifiedSince, IfMatch,
// IfNoneMatch, AcceptEncoding. For more information, check out the following link:
// https://help.aliyun.com/document_detail/oss/api-reference/object/GetObject.html
//
// io.ReadCloser  reader object for getting the data from response. It needs be closed after the usage. It's only valid when error is nill.
// error  It's nil if no errors; otherwise it's an error object.
//
func (bucket Bucket) GetObjectWithURL(signedURL string, options ...Option) (io.ReadCloser, error) {
	result, err := bucket.DoGetObjectWithURL(signedURL, options)
	if err != nil {
		return nil, err
	}
	return result.Response.Body, nil
}

//
// GetObjectToFile Download the object into a local file with the signed url.
//
// signedURL  signed url
// filePath   The local file path to download to.
// options    The options for downloading object. Checks out the parameter options in function GetObject for the reference.
//
// error  It's nil if no errors; otherwise it's an error object.
//
func (bucket Bucket) GetObjectToFileWithURL(signedURL, filePath string, options ...Option) error {
	tempFilePath := filePath + TempFileSuffix

	// gets the object's content
	result, err := bucket.DoGetObjectWithURL(signedURL, options)
	if err != nil {
		return err
	}
	defer result.Response.Body.Close()

	// if the file does not exist, create one. If exists, then overwrite it.
	fd, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, FilePermMode)
	if err != nil {
		return err
	}

	// saves the data to the file.
	_, err = io.Copy(fd, result.Response.Body)
	fd.Close()
	if err != nil {
		return err
	}

	// compares the CRC value. If CRC values do not match, return error.
	hasRange, _, _ := isOptionSet(options, HTTPHeaderRange)
	if bucket.getConfig().IsEnableCRC && !hasRange {
		result.Response.ClientCRC = result.ClientCRC.Sum64()
		err = checkCRC(result.Response, "GetObjectToFileWithURL")
		if err != nil {
			os.Remove(tempFilePath)
			return err
		}
	}

	return os.Rename(tempFilePath, filePath)
}

//
// DoGetObjectWithURL The actual API that downloads the file with the signed url.
//
// signedURL  Signed URL.
// options    The options for getting object. Check out parameter options in GetObject for the reference.
//
// GetObjectResult The result object when the error is nil.
// error  It's nil if no errors; otherwise it's an error object.
//
func (bucket Bucket) DoGetObjectWithURL(signedURL string, options []Option) (*GetObjectResult, error) {
	params := map[string]interface{}{}
	resp, err := bucket.doURL("GET", signedURL, params, options, nil, nil)
	if err != nil {
		return nil, err
	}

	result := &GetObjectResult{
		Response: resp,
	}

	// crc
	var crcCalc hash.Hash64
	hasRange, _, _ := isOptionSet(options, HTTPHeaderRange)
	if bucket.getConfig().IsEnableCRC && !hasRange {
		crcCalc = crc64.New(crcTable())
		result.ServerCRC = resp.ServerCRC
		result.ClientCRC = crcCalc
	}

	// progress
	listener := getProgressListener(options)

	contentLen, _ := strconv.ParseInt(resp.Headers.Get(HTTPHeaderContentLength), 10, 64)
	resp.Body = ioutil.NopCloser(TeeReader(resp.Body, crcCalc, contentLen, listener, nil))

	return result, nil
}

// Private
func (bucket Bucket) do(method, objectName string, params map[string]interface{}, options []Option,
	data io.Reader, listener ProgressListener) (*Response, error) {
	headers := make(map[string]string)
	err := handleOptions(headers, options)
	if err != nil {
		return nil, err
	}
	return bucket.Client.Conn.Do(method, bucket.BucketName, objectName,
		params, headers, data, 0, listener)
}

func (bucket Bucket) doURL(method HTTPMethod, signedURL string, params map[string]interface{}, options []Option,
	data io.Reader, listener ProgressListener) (*Response, error) {
	headers := make(map[string]string)
	err := handleOptions(headers, options)
	if err != nil {
		return nil, err
	}
	return bucket.Client.Conn.DoURL(method, signedURL, headers, data, 0, listener)
}

func (bucket Bucket) getConfig() *Config {
	return bucket.Client.Config
}

func addContentType(options []Option, keys ...string) []Option {
	typ := TypeByExtension("")
	for _, key := range keys {
		typ = TypeByExtension(key)
		if typ != "" {
			break
		}
	}

	if typ == "" {
		typ = "application/octet-stream"
	}

	opts := []Option{ContentType(typ)}
	opts = append(opts, options...)

	return opts
}
