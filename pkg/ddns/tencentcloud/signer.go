package tencentcloud

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// https://github.com/jeessy2/ddns-go/blob/master/util/tencent_cloud_signer.go
func SignRequest(secretId string, secretKey string, r *http.Request, action string, payload string) {
	algorithm := "TC3-HMAC-SHA256"
	service := "dnspod"
	host := writeString(service, ".tencentcloudapi.com")
	timestamp := time.Now().Unix()
	timestampStr := strconv.FormatInt(timestamp, 10)

	// 步骤 1：拼接规范请求串
	canonicalHeaders := writeString("content-type:application/json\nhost:", host, "\nx-tc-action:", strings.ToLower(action), "\n")
	signedHeaders := "content-type;host;x-tc-action"
	hashedRequestPayload := sha256hex(payload)
	canonicalRequest := writeString("POST\n/\n\n", canonicalHeaders, "\n", signedHeaders, "\n", hashedRequestPayload)

	// 步骤 2：拼接待签名字符串
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	credentialScope := writeString(date, "/", service, "/tc3_request")
	hashedCanonicalRequest := sha256hex(canonicalRequest)
	string2sign := writeString(algorithm, "\n", timestampStr, "\n", credentialScope, "\n", hashedCanonicalRequest)

	// 步骤 3：计算签名
	secretDate := hmacsha256(date, writeString("TC3", secretKey))
	secretService := hmacsha256(service, secretDate)
	secretSigning := hmacsha256("tc3_request", secretService)
	signature := hex.EncodeToString([]byte(hmacsha256(string2sign, secretSigning)))

	// 步骤 4：拼接 Authorization
	authorization := writeString(algorithm, " Credential=", secretId, "/", credentialScope, ", SignedHeaders=", signedHeaders, ", Signature=", signature)

	r.Header.Set("Authorization", authorization)
	r.Header.Set("Host", host)
	r.Header.Set("X-TC-Action", action)
	r.Header.Set("X-TC-Timestamp", timestampStr)
}

func sha256hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func hmacsha256(s, key string) string {
	hashed := hmac.New(sha256.New, []byte(key))
	hashed.Write([]byte(s))
	return string(hashed.Sum(nil))
}

func writeString(strs ...string) string {
	var b strings.Builder
	for _, str := range strs {
		b.WriteString(str)
	}

	return b.String()
}
