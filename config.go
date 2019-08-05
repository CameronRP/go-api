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
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/afero"
	yaml "gopkg.in/yaml.v2"
)

const (
	DeviceConfigPath     = "/etc/cacophony/device.yaml"
	RegisteredConfigPath = "/etc/cacophony/device-priv.yaml"
	hostnameFile         = "/etc/hostname"
	hostsFile            = "/etc/hosts"
	hostsFileFormat      = "127.0.0.1\t%s"
)

type Config struct {
	ServerURL  string `yaml:"server-url" json:"serverURL"`
	Group      string `yaml:"group" json:"groupname"`
	DeviceName string `yaml:"device-name" json:"devicename"`
	filePath   string
}

func GetConfig(filePath string) (*Config, error) {
	if exists, err := afero.Exists(Fs, filePath); err != nil {
		return nil, err
	} else if !exists {
		return nil, notRegisteredError
	}

	conf := &Config{
		filePath: filePath,
	}
	if err := conf.read(); err != nil {
		return nil, err
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return conf, nil
}

func (c *Config) read() error {
	buf, err := afero.ReadFile(Fs, c.filePath)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(buf, c)
}

func (c *Config) write() error {
	buf, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return afero.WriteFile(Fs, c.filePath, buf, 0644)
}

func (c *Config) exists() (bool, error) {
	return afero.Exists(Fs, c.filePath)
}

func updateConfNameAndGroup(newdevice string, newgroup string, filePath string) error {
	conf, err := GetConfig(filePath)
	if err != nil {
		return err
	}
	conf.DeviceName = newdevice
	conf.Group = newgroup
	return conf.write()
}

func updateHostnameFiles(hostname string) error {
	if err := afero.WriteFile(Fs, hostnameFile, []byte(hostname), 0644); err != nil {
		return err
	}

	input, err := afero.ReadFile(Fs, hostsFile)
	if err != nil {
		return err
	}

	lines := strings.Split(string(input), "\n")

	for i, line := range lines {
		if strings.HasPrefix(line, "127.0.0.1") {
			lines[i] = fmt.Sprintf(hostsFileFormat, hostname)
		}
	}
	output := strings.Join(lines, "\n")
	return afero.WriteFile(Fs, hostsFile, []byte(output), 0644)

}

//Validate checks supplied Config contains the required data
func (conf *Config) Validate() error {
	if conf.ServerURL == "" {
		return errors.New("server-url missing")
	}

	if conf.DeviceName == "" {
		return errors.New("device-name missing")
	}
	return nil
}

type PrivateConfig struct {
	Password string `yaml:"password"`
	DeviceID int    `yaml:"device-id" json:"deviceID"`
}

//Validate checks supplied Config contains the required data
func (conf *PrivateConfig) IsValid() bool {
	return conf.Password != "" && conf.DeviceID != 0
}

const (
	lockfile       = "/var/lock/go-api-priv.lock"
	lockRetryDelay = 678 * time.Millisecond
	lockTimeout    = 5 * time.Second
)

// LoadPrivateConfig acquires a readlock and reads private config
func LoadPrivateConfig() (*PrivateConfig, error) {
	lockSafeConfig := NewLockSafeConfig(RegisteredConfigPath)
	return lockSafeConfig.Read()
}

type LockSafeConfig struct {
	fileLock *flock.Flock
	filename string
	config   *PrivateConfig
}

func NewLockSafeConfig(filename string) *LockSafeConfig {
	return &LockSafeConfig{
		filename: filename,
		fileLock: flock.New(lockfile),
	}
}

func (lockSafeConfig *LockSafeConfig) Unlock() {
	lockSafeConfig.fileLock.Unlock()
}

// GetExLock acquires an exclusive lock on confPassword
func (lockSafeConfig *LockSafeConfig) GetExLock() (bool, error) {
	lockCtx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	locked, err := lockSafeConfig.fileLock.TryLockContext(lockCtx, lockRetryDelay)
	return locked, err
}

// getReadLock  acquires a read lock on the supplied Flock struct
func getReadLock(fileLock *flock.Flock) (bool, error) {
	lockCtx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()
	locked, err := fileLock.TryRLockContext(lockCtx, lockRetryDelay)
	return locked, err
}

// ReadPassword acquires a readlock and reads the config
func (lockSafeConfig *LockSafeConfig) Read() (*PrivateConfig, error) {
	locked := lockSafeConfig.fileLock.Locked()
	if locked == false {
		locked, err := getReadLock(lockSafeConfig.fileLock)
		if locked == false || err != nil {
			return nil, err
		}
		defer lockSafeConfig.Unlock()
	}

	buf, err := afero.ReadFile(Fs, lockSafeConfig.filename)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(buf, &lockSafeConfig.config); err != nil {
		return nil, err
	}
	return lockSafeConfig.config, nil
}

// WritePassword checks the file is locked and writes the password
func (lockSafeConfig *LockSafeConfig) Write(deviceID int, password string) error {
	conf := PrivateConfig{DeviceID: deviceID, Password: password}
	buf, err := yaml.Marshal(&conf)
	if err != nil {
		return err
	}
	if lockSafeConfig.fileLock.Locked() {
		err = afero.WriteFile(Fs, lockSafeConfig.filename, buf, 0600)
	} else {
		return fmt.Errorf("file is not locked %v", lockSafeConfig.filename)
	}
	return err
}

var Fs = afero.NewOsFs()
