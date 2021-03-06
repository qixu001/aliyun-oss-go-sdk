package sample

import (
	"fmt"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// BucketCORSSample demos how to get or set the bucket CORS.
func BucketCORSSample() {
	// New Client
	client, err := oss.New(endpoint, accessID, accessKey)
	if err != nil {
		HandleError(err)
	}

	// creates the bucket with default parameters
	err = client.CreateBucket(bucketName)
	if err != nil {
		HandleError(err)
	}

	rule1 := oss.CORSRule{
		AllowedOrigin: []string{"*"},
		AllowedMethod: []string{"PUT", "GET", "POST"},
		AllowedHeader: []string{},
		ExposeHeader:  []string{},
		MaxAgeSeconds: 100,
	}

	rule2 := oss.CORSRule{
		AllowedOrigin: []string{"http://www.a.com", "http://www.b.com"},
		AllowedMethod: []string{"GET"},
		AllowedHeader: []string{"Authorization"},
		ExposeHeader:  []string{"x-oss-test", "x-oss-test1"},
		MaxAgeSeconds: 100,
	}

	// case 1：sets the bucket CORS rules
	err = client.SetBucketCORS(bucketName, []oss.CORSRule{rule1})
	if err != nil {
		HandleError(err)
	}

	// case 3：sets the bucket CORS rules. if CORS rules exist, they will be overwritten.
	err = client.SetBucketCORS(bucketName, []oss.CORSRule{rule1, rule2})
	if err != nil {
		HandleError(err)
	}

	// gets the bucket's CORS
	gbl, err := client.GetBucketCORS(bucketName)
	if err != nil {
		HandleError(err)
	}
	fmt.Println("Bucket CORS:", gbl.CORSRules)

	// deletes Bucket's CORS
	err = client.DeleteBucketCORS(bucketName)
	if err != nil {
		HandleError(err)
	}

	// deletes bucket
	err = client.DeleteBucket(bucketName)
	if err != nil {
		HandleError(err)
	}

	fmt.Println("BucketCORSSample completed")
}
