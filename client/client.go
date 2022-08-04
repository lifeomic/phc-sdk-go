package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

type payload struct {
	Headers               map[string]string `json:"headers"`
	Path                  string            `json:"path"`
	HttpMethod            string            `json:"httpMethod"`
	QueryStringParameters map[string]string `json:"queryStringParameters"`
	Body                  string            `json:"body"`
}

type policy struct {
	Rules map[string]bool `json:"rules"`
}

type responsePayload struct {
	Body       string            `json:"body"`
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
}

func toHeader(header map[string]string) http.Header {
	result := make(http.Header)
	for k, v := range header {
		result[k] = []string{v}
	}
	return result
}

type responseBody struct {
	Data   map[string]interface{} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type Invoker interface {
	Invoke(context.Context, *lambda.InvokeInput, ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

type Client struct {
	invoker Invoker
	account string
	user    string
	rules   map[string]bool
}

func (c *Client) buildHeaders() map[string]string {
	policy, _ := json.Marshal(&policy{
		Rules: c.rules,
	})
	return map[string]string{
		"LifeOmic-Account": c.account,
		"LifeOmic-User":    c.user,
		"content-type":     "application/json",
		"LifeOmic-Policy":  string(policy),
	}
}

func (c *Client) buildGqlQuery(path string, query string, variables map[string]interface{}) []byte {
	type Body struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	body, _ := json.Marshal(&Body{Query: query, Variables: variables})
	payload := &payload{
		Headers:               c.buildHeaders(),
		HttpMethod:            "POST",
		QueryStringParameters: map[string]string{},
		Path:                  path,
		Body:                  string(body),
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("Failed to marshall payload %v", err)
	}
	return bytes
}

func parseUri(uri string) (*string, *string, error) {
	index := strings.IndexAny(uri, "/")
	if index == -1 {
		return nil, nil, errors.New("Invalid URL provided")
	}
	functionName := uri[0:index]
	path := uri[index:]
	return &functionName, &path, nil
}

func (c *Client) Gql(uri string, query string, variables map[string]interface{}) (*map[string]interface{}, error) {
	functionName, path, err := parseUri(uri)
	if err != nil {
		return nil, err
	}
	// MP_ARN := "marketplace-service:deployed"
	resp, err := c.invoker.Invoke(context.Background(), &lambda.InvokeInput{
		FunctionName: functionName,
		Payload:      c.buildGqlQuery(*path, query, variables),
	})

	if err != nil {
		return nil, err
	}
	var payload responsePayload
	err = json.Unmarshal(resp.Payload, &payload)
	if err != nil {
		return nil, err
	}

	var body responseBody
	err = json.Unmarshal([]byte(payload.Body), &body)
	if err != nil {
		return nil, err
	}
	if len(body.Errors) > 0 {
		return nil, errors.New(body.Errors[0].Message)
	}
	return &body.Data, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	functionName, path, err := parseUri(req.URL.String())
	if err != nil {
		return nil, err
	}

	// Copy additional headers from the req struct into lambda request headers
	// go http.Header type doesn't align with the lambda header type
	// so we just take the first value of the request header
	headers := c.buildHeaders()
	for k, v := range req.Header {
		if _, ok := headers[k]; !ok {
			headers[k] = v[0]
		}
	}

	body, err := ioutil.ReadAll(req.Body)

	data, err := json.Marshal(payload{
		Headers:               headers,
		HttpMethod:            req.Method,
		QueryStringParameters: map[string]string{},
		Path:                  *path,
		Body:                  string(body),
	})

	lambdaResponse, err := c.invoker.Invoke(req.Context(), &lambda.InvokeInput{
		FunctionName: functionName,
		Payload:      data,
	})

	if err != nil {
		return nil, err
	}

	// attempt to convert lambda response into http Response
	var respPayload responsePayload
	err = json.Unmarshal(lambdaResponse.Payload, &respPayload)

	if err != nil {
		return nil, err
	}

	resp := http.Response{
		Body:       ioutil.NopCloser(bytes.NewBuffer([]byte(respPayload.Body))),
		StatusCode: respPayload.StatusCode,
		Header:     toHeader(respPayload.Headers),
	}

	return &resp, nil
}

func BuildClient(account string, user string, rules map[string]bool) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, err
	}
	client := Client{invoker: lambda.NewFromConfig(cfg), user: user, rules: rules, account: account}
	return &client, nil
}
