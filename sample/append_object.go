// Package sample examples
package sample

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// AppendObjectSample demo the append file's usage
func AppendObjectSample() {
	// 创建Bucket
	bucket, err := GetTestBucket(bucketName)
	if err != nil {
		HandleError(err)
	}

	err = bucket.DeleteObject(objectKey)

	var str = "弃我去者，昨日之日不可留。 乱我心者，今日之日多烦忧！"
	var nextPos int64

	// case 1: append a string to the object
	// the first append position is 0 and the return value is for the next append's position.
	nextPos, err = bucket.AppendObject(objectKey, strings.NewReader(str), nextPos)
	if err != nil {
		HandleError(err)
	}

	// second append
	nextPos, err = bucket.AppendObject(objectKey, strings.NewReader(str), nextPos)
	if err != nil {
		HandleError(err)
	}

	// download
	body, err := bucket.GetObject(objectKey)
	if err != nil {
		HandleError(err)
	}
	data, err := ioutil.ReadAll(body)
	body.Close()
	if err != nil {
		HandleError(err)
	}
	fmt.Println(objectKey, ":", string(data))

	err = bucket.DeleteObject(objectKey)
	if err != nil {
		HandleError(err)
	}

	// case 2：append byte array to the object
	nextPos = 0
	// the first append position is 0, and the return value is for the next append's position.
	nextPos, err = bucket.AppendObject(objectKey, bytes.NewReader([]byte(str)), nextPos)
	if err != nil {
		HandleError(err)
	}

	// second append
	nextPos, err = bucket.AppendObject(objectKey, bytes.NewReader([]byte(str)), nextPos)
	if err != nil {
		HandleError(err)
	}

	// download
	body, err = bucket.GetObject(objectKey)
	if err != nil {
		HandleError(err)
	}
	data, err = ioutil.ReadAll(body)
	body.Close()
	if err != nil {
		HandleError(err)
	}
	fmt.Println(objectKey, ":", string(data))

	err = bucket.DeleteObject(objectKey)
	if err != nil {
		HandleError(err)
	}

	//case 3：append a local file to the object
	fd, err := os.Open(localFile)
	if err != nil {
		HandleError(err)
	}
	defer fd.Close()

	nextPos = 0
	nextPos, err = bucket.AppendObject(objectKey, fd, nextPos)
	if err != nil {
		HandleError(err)
	}

	// case 4，get the next append position by GetObjectDetailedMeta
	props, err := bucket.GetObjectDetailedMeta(objectKey)
	nextPos, err = strconv.ParseInt(props.Get(oss.HTTPHeaderOssNextAppendPosition), 10, 0)
	if err != nil {
		HandleError(err)
	}

	nextPos, err = bucket.AppendObject(objectKey, strings.NewReader(str), nextPos)
	if err != nil {
		HandleError(err)
	}

	err = bucket.DeleteObject(objectKey)
	if err != nil {
		HandleError(err)
	}

	// case 5：Specifies the object properties for the first append, including the "x-oss-meta"'s custom metadata.
	options := []oss.Option{
		oss.Expires(futureDate),
		oss.ObjectACL(oss.ACLPublicRead),
		oss.Meta("myprop", "mypropval")}
	nextPos = 0
	fd.Seek(0, os.SEEK_SET)
	nextPos, err = bucket.AppendObject(objectKey, strings.NewReader(str), nextPos, options...)
	if err != nil {
		HandleError(err)
	}
	// second append.
	fd.Seek(0, os.SEEK_SET)
	nextPos, err = bucket.AppendObject(objectKey, strings.NewReader(str), nextPos)
	if err != nil {
		HandleError(err)
	}

	props, err = bucket.GetObjectDetailedMeta(objectKey)
	if err != nil {
		HandleError(err)
	}
	fmt.Println("myprop:", props.Get("x-oss-meta-myprop"))

	goar, err := bucket.GetObjectACL(objectKey)
	if err != nil {
		HandleError(err)
	}
	fmt.Println("Object ACL:", goar.ACL)

	// deletes the object and bucket
	err = DeleteTestBucketAndObject(bucketName)
	if err != nil {
		HandleError(err)
	}

	fmt.Println("AppendObjectSample completed")
}
