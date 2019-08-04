package xhttpc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/ioutil"
	"mime/multipart"
	"os"
)

type XResponse struct {
	*http.Response
}

func (resp *XResponse) DecodeJson(v interface{}) error {
	var reader io.ReadCloser
	var err error
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
	default:
		reader = resp.Body
	}
	defer func(r io.ReadCloser) {
		_ = r.Close()
	}(reader)

	decErr := json.NewDecoder(reader).Decode(v)
	if decErr == io.EOF {
		// ignore EOF errors caused by empty response body
		decErr = nil
	}
	if decErr != nil {
		return decErr
	}
	return nil
}

func (resp *XResponse) Copy(w io.Writer) (written int64, err error) {
	written, err = io.Copy(w, resp.Body)
	return
}

func (resp *XResponse) ReadAll() ([]byte, error) {
	var reader io.ReadCloser
	var err error
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
	default:
		reader = resp.Body
	}
	defer func(r io.ReadCloser) {
		_ = r.Close()
	}(reader)
	return ioutil.ReadAll(reader)
}

func (resp *XResponse) String() (string, error) {
	v, err := resp.ReadAll()
	if err != nil {
		return "", err
	}
	return string(v), nil
}

type Header map[string]string

func (h Header) Set(k, v string) {
	h[k] = v
}

func NewXClient(client *http.Client) *XClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &XClient{
		Client:     client,
		BaseHeader: make(map[string]string),
		BaseQuery:  url.Values{},
	}
}

type XClient struct {
	*http.Client
	BaseHeader Header
	BaseQuery  url.Values
}

func (c *XClient) Post(ctx context.Context, u *url.URL, body interface{}, header Header) (*XResponse, error) {
	values, err := toValues(body)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, http.MethodPost, c.resolveURL(u), values, header)
}

func (c *XClient) Put(ctx context.Context, u *url.URL, body interface{}, header Header) (*XResponse, error) {
	values, err := toValues(body)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, http.MethodPut, c.resolveURL(u), values, header)
}

func (c *XClient) Delete(ctx context.Context, u *url.URL, query interface{}, header Header) (res *XResponse, err error) {
	values, err := toValues(query)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, http.MethodDelete, c.resolveURL(u, values), nil, header)
}

func (c *XClient) Get(ctx context.Context, u *url.URL, query interface{}, header Header) (res *XResponse, err error) {
	values, err := toValues(query)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, http.MethodGet, c.resolveURL(u, values), nil, header)
}

func (c *XClient) call(ctx context.Context, method, url string, body url.Values, header Header) (*XResponse, error) {
	req, err := c.NewRequest(method, url, body, header)
	if err != nil {
		return nil, err
	}
	resp, err := c.XDo(ctx, req)
	if err != nil {
		return resp, err
	}
	return resp, nil
}

func (c *XClient) NewRequest(method, url string, body url.Values, header Header) (*http.Request, error) {
	var buf io.Reader
	if body != nil {
		buf = strings.NewReader(body.Encode())
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		return nil, err
	}
	for k, v := range c.BaseHeader {
		req.Header.Set(k, v)
	}
	if header != nil {
		for k, v := range header {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

func (c *XClient) NewUploadRequest(url string, reader io.Reader, size int64, mediaType string, header Header) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, url, reader)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size

	for k, v := range c.BaseHeader {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", mediaType)
	if header != nil {
		for k, v := range header {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

func (c *XClient) NewMultipartRequest(url string, values map[string]io.Reader, header Header) (*http.Request, error) {
	var buffer bytes.Buffer
	multipartWriter := multipart.NewWriter(&buffer)
	for key, reader := range values {
		var fieldWriter io.Writer
		var err error = nil
		if closable, ok := reader.(io.Closer); ok {
			_ = closable.Close()
		}
		if file, ok := reader.(*os.File); ok {
			if fieldWriter, err = multipartWriter.CreateFormFile(key, file.Name()); err != nil {
				return nil, err
			}
		} else {
			if fieldWriter, err = multipartWriter.CreateFormField(key); err != nil {
				return nil, err
			}
		}
		if _, err = io.Copy(fieldWriter, reader); err != nil {
			return nil, err
		}
	}
	_ = multipartWriter.Close()
	req, err := http.NewRequest(http.MethodPost, url, &buffer)
	if err != nil {
		return nil, err
	}
	for k, v := range c.BaseHeader {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	if header != nil {
		for k, v := range header {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

func (c *XClient) XDo(ctx context.Context, req *http.Request) (*XResponse, error) {
	req = req.WithContext(ctx)
	resp, err := c.Client.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return nil, err
	}

	return &XResponse{Response: resp}, nil
}

func (c *XClient) resolveURL(u *url.URL, queries ...url.Values) string {
	q := url.Values{}
	for _, query := range queries {
		keys := make([]string, 0, len(query))
		for k := range query {
			keys = append(keys, k)
		}
		for _, k := range keys {
			vs := query.Get(k)
			q.Add(k, vs)
		}
	}
	u.RawQuery = q.Encode()
	baseQuery := c.BaseQuery.Encode()
	rawURL := u.String()
	if baseQuery == "" {
		return rawURL
	}
	if u.RawQuery == "" {
		return rawURL + "?" + baseQuery
	}
	return rawURL + "&" + baseQuery
}

func toValues(data interface{}) (url.Values, error) {
	if data == nil {
		return url.Values{}, nil
	}
	result := make(map[string]interface{})
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if err := d.Decode(&result); err != nil {
		return nil, err
	}
	values := url.Values{}
	for k, v := range result {
		addValues(values, k, v)
	}
	return values, nil
}

func addValues(values url.Values, k string, v interface{}) {
	switch as := v.(type) {
	case []interface{}:
		for _, v := range as {
			addValues(values, k, v)
		}
	case map[string]interface{}:
		for mapKey, v := range as {
			addValues(values, fmt.Sprintf(k, mapKey), v)
		}
	default:
		values.Add(k, fmt.Sprintf("%v", v))
	}
}
