// go-api - Client for the Cacophony API server.
// Copyright (C) 2018, The Cacophony Project
//
//Licensed under the Apache License, Version 2.0 (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
//Unless required by applicable law or agreed to in writing, software
//distributed under the License is distributed on an "AS IS" BASIS,
//WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//See the License for the specific language governing permissions and
//limitations under the License.

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

type CacophonyClient struct {
	group          string
	name           string
	password       string
	token          string
	justRegistered bool
}

type CacophonyAPI struct {
	Client     *CacophonyClient
	httpClient *http.Client
	serverURL  string
	regURL     string
	authURL    string
}

func (api *CacophonyAPI) Password() string {
	return api.Client.password
}

func (api *CacophonyAPI) JustRegistered() bool {
	return api.Client.justRegistered
}

const httpTimeout = 60 * time.Second
const timeout = 30 * time.Second
const basePath = "/api/v1"

// NewAPI creates a CacophonyAPI instance and obtains a fresh JSON Web
// Token. If no password is given then the device is registered.
func NewAPI(serverURL, group, deviceName, password string) (*CacophonyAPI, error) {

	if deviceName == "" {
		return nil, errors.New("no device name")
	}

	client := &CacophonyClient{
		group:    group,
		name:     deviceName,
		password: password,
	}

	api := &CacophonyAPI{
		serverURL:  serverURL,
		Client:     client,
		httpClient: newHTTPClient(),
		regURL:     serverURL + basePath + "/devices",
		authURL:    serverURL + "/authenticate_device",
	}

	//api.typeName = deviceName
	//	api.regURL = api.serverURL + basePath + "/devices"
	//api.authURL = api.serverURL + "/authenticate_device"

	if client.password == "" {
		err := api.register()
		if err != nil {
			return nil, err
		}
	} else {
		err := api.authenticate()
		if err != nil {
			return nil, err
		}
	}
	return api, nil
}

// authenticate authenticates the device with the api server
// and retrieves the token
func (api *CacophonyAPI) authenticate() error {

	if api.Client.password == "" {
		return errors.New("no password set")
	}
	payload, err := json.Marshal(map[string]string{
		"devicename": api.Client.name,
		"password":   api.Client.password,
	})
	if err != nil {
		return err
	}
	postResp, err := api.httpClient.Post(
		api.authURL,
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()

	if err := handleHTTPResponse(postResp); err != nil {
		return err
	}

	var resp tokenResponse
	d := json.NewDecoder(postResp.Body)
	if err := d.Decode(&resp); err != nil {
		return fmt.Errorf("decode: %v", err)
	}
	if !resp.Success {
		return fmt.Errorf("failed getting new token: %v", resp.message())
	}
	api.Client.token = resp.Token
	return nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   timeout, // connection timeout
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,

			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          5,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

//register a device on the cacophony server and retrieves it's token
func (api *CacophonyAPI) register() error {
	if api.Client.password != "" {
		return errors.New("already registered")
	}

	password := randString(20)
	payload, err := json.Marshal(map[string]string{
		"group":      api.Client.group,
		"devicename": api.Client.name,
		"password":   password,
	})
	if err != nil {
		return err
	}
	postResp, err := api.httpClient.Post(
		api.regURL,
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()

	if err := handleHTTPResponse(postResp); err != nil {
		return err
	}

	var respData tokenResponse
	d := json.NewDecoder(postResp.Body)
	if err := d.Decode(&respData); err != nil {
		return fmt.Errorf("decode: %v", err)
	}
	if !respData.Success {
		return fmt.Errorf("registration failed: %v", respData.message())
	}

	api.Client.password = password
	api.Client.token = respData.Token
	api.Client.justRegistered = true

	return nil
}

// UploadThermalRaw uploads the file to the api server as a multipartmessage
// with data of type thermalRaw specified
func (api *CacophonyAPI) UploadThermalRaw(r io.Reader) error {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)

	// JSON encoded "data" parameter.
	dataBuf, err := json.Marshal(map[string]string{
		"type": "thermalRaw",
	})
	if err != nil {
		return err
	}
	if err := w.WriteField("data", string(dataBuf)); err != nil {
		return err
	}

	// Add the file as a new MIME part.
	fw, err := w.CreateFormFile("file", "file")
	if err != nil {
		return err
	}
	io.Copy(fw, r)
	w.Close()

	req, err := http.NewRequest("POST", api.serverURL+basePath+"/recordings", buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", api.Client.token)

	resp, err := api.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := handleHTTPResponse(resp); err != nil {
		return err
	}

	return nil
}

type tokenResponse struct {
	Success  bool
	Messages []string
	Token    string
}

func (r *tokenResponse) message() string {
	if len(r.Messages) > 0 {
		return r.Messages[0]
	}
	return "unknown"
}

func (api *CacophonyAPI) getFileFromJWT(jwt, path string) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	// Get the data
	resp, err := http.Get(api.serverURL + basePath + "/signedUrl?jwt=" + jwt)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check server response
	if err := handleHTTPResponse(resp); err != nil {
		return err
	}

	// Writer the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

type FileResponse struct {
	File FileInfo
	Jwt  string
}

type FileInfo struct {
	Details FileDetails
	Type    string
}

type FileDetails struct {
	Name         string
	OriginalName string
}

// GetFileDetails will download the file details from the files api.  This can then be parsed into
// DownloadFile to download the file
func (api *CacophonyAPI) GetFileDetails(fileID int) (*FileResponse, error) {
	buf := new(bytes.Buffer)

	req, err := http.NewRequest("GET", api.serverURL+basePath+"/files/"+strconv.Itoa(fileID), buf)
	req.Header.Set("Authorization", api.Client.token)
	//client := new(http.Client)

	resp, err := api.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var fr FileResponse
	d := json.NewDecoder(resp.Body)
	if err := d.Decode(&fr); err != nil {
		return &fr, err
	}
	return &fr, nil
}

// DownloadFile will take the file details from GetFileDetails and download the file to a specified path
func (api *CacophonyAPI) DownloadFile(fileResponse *FileResponse, filePath string) error {
	if _, err := os.Stat(filePath); err == nil {
		return err
	}

	return api.getFileFromJWT(fileResponse.Jwt, filePath)
}

func (api *CacophonyAPI) ReportEvent(jsonDetails []byte, times []time.Time) error {
	// Deserialise the JSON event details into a map.
	var details map[string]interface{}
	err := json.Unmarshal(jsonDetails, &details)
	if err != nil {
		return err
	}

	// Convert the event times for sending and add to the map to send.
	dateTimes := make([]string, 0, len(times))
	for _, t := range times {
		dateTimes = append(dateTimes, formatTimestamp(t))
	}
	details["dateTimes"] = dateTimes

	// Serialise the map back to JSON for sending.
	jsonAll, err := json.Marshal(details)
	if err != nil {
		return err
	}

	// Prepare request.
	req, err := http.NewRequest("POST", api.serverURL+basePath+"/events", bytes.NewReader(jsonAll))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", api.Client.token)

	// Send.
	//client := &http.Client{Timeout: httpTimeout}
	resp, err := api.httpClient.Do(req)
	if err != nil {
		return temporaryError(err)
	}
	defer resp.Body.Close()

	if err := handleHTTPResponse(resp); err != nil {
		return err
	}

	return nil
}

func handleHTTPResponse(resp *http.Response) error {
	if !(isHTTPSuccess(resp.StatusCode)) {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return temporaryError(fmt.Errorf("request failed (%d) and body read failed: %v", resp.StatusCode, err))
		}
		return &Error{
			message:   fmt.Sprintf("HTTP request failed (%d): %s", resp.StatusCode, body),
			permanent: isHTTPClientError(resp.StatusCode),
		}
	}
	return nil
}

func formatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func isHTTPSuccess(code int) bool {
	return code >= 200 && code < 300
}

func isHTTPClientError(code int) bool {
	return code >= 400 && code < 500
}

// GetSchedule will get the audio schedule
func (api *CacophonyAPI) GetSchedule() ([]byte, error) {
	req, err := http.NewRequest("GET", api.serverURL+basePath+"schedules", nil)
	req.Header.Set("Authorization", api.Client.token)
	//client := new(http.Client)

	resp, err := api.httpClient.Do(req)
	if err != nil {
		return []byte{}, err
	}
	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}
