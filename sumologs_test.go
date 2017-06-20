package sumologs

import (
	"bytes"
	// "fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker/daemon/logger"

	// run "go get gopkg.in/jarcoal/httpmock.v1" before running tests
	"gopkg.in/jarcoal/httpmock.v1"
)

const (
	sumoUrl = "https://stag-events.sumologic.net/receiver/v1/http/ZaVnC4dhaV27p86wEkCthvRI4IUASAom3K3-y2qQI8aLQuMKT8wL4yrXruk4ak1UUk10h4LY7-w9Jcb1yb7a5rSdx2-KkLN48eeyR6eqE17ygZut36dfJQ=="
	sumoUrlMock = "https://fake.sumo.Url"
	badUrl = "https://bad.Url"
)

func TestValidateLogOpt(t *testing.T) {
	assert := assert.New(t)
	err := ValidateLogOpt(map[string]string{
		logOptUrl : sumoUrl,
	})
	assert.Nil(err)

	err = ValidateLogOpt(map[string]string{
		"bad-option" : "bad-option-value",
	})
	assert.NotNil(err, "should fail when user enters unsupported option")

	err = ValidateLogOpt(map[string]string{
		logOptUrl : "",
	})
	assert.NotNil(err, "should fail when user enters empty %s", logOptUrl)
}

func TestNew(t *testing.T) {
	assert := assert.New(t)
	t.Run("missing sumo-url", func(t *testing.T) {
		info := logger.Info{
			Config: map[string]string{},
		}
		_, err := New(info)

		assert.NotNil(err, "should fail when sumo-url not provided")
	})
	t.Run("configured correctly", func(t *testing.T) {
		info := logger.Info{
			Config: map[string]string{
				"sumo-url" : sumoUrl,
			},
		}

		s, err := New(info)
		assert.Nil(err, "should not fail when configured correctly")
		
		assert.Equal(driverName, s.Name(), "name of logging driver is incorrect")
		
		err = s.Close()
		assert.Nil(err, "should not fail when calling Close() on logger")
	})
}

func TestDefaultSettings(t *testing.T) {
	assert := assert.New(t)
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	var messages []string

	httpmock.RegisterResponder("POST", sumoUrlMock,
		func(req *http.Request) (*http.Response, error) {
			buf := new(bytes.Buffer)
			buf.ReadFrom(req.Body)
			message := buf.String()

			messages = append(messages, message)
			resp := httpmock.NewStringResponse(200, message)
			return resp, nil
		},
	)
	info := logger.Info{
		Config: map[string]string{
			"sumo-url" : sumoUrlMock,
		},
	}

	s, err := New(info)
	assert.Nil(err, "should not fail when calling New()")

	msgStrings := []string{"hello", "", "This is a log with\na 2nd line and a #!"}

	var msg *logger.Message
	for _, msgString := range msgStrings {
		msg = &logger.Message{Line: []byte(msgString), Source: "stdout", Timestamp: time.Now()}
		err = s.Log(msg)
		assert.Nil(err, "should not fail when calling Log(). sent %s", msgString)
	}

	err = s.Close()
	assert.Nil(err, "should not fail when calling Close()")

	assert.Equal(3, len(messages), "should have received 3 logs")
	for i := 0; i < 3; i++ {
		assert.Equal(msgStrings[i], messages[i], "message is incorrect")
	}
}

func TestDefaultSettingsBadUrl(t *testing.T) {
	assert := assert.New(t)
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	var messages []string

	httpmock.RegisterResponder("POST", sumoUrlMock,
		func(req *http.Request) (*http.Response, error) {
			buf := new(bytes.Buffer)
			buf.ReadFrom(req.Body)
			message := buf.String()

			messages = append(messages, message)
			resp := httpmock.NewStringResponse(200, message)
			return resp, nil
		},
	)
	info := logger.Info{
		Config: map[string]string{
			"sumo-url" : badUrl,
		},
	}

	s, err := New(info)
	assert.Nil(err, "should not fail when calling New()")

	msgStrings := []string{"hello", "", "This is a log with\na 2nd line and a #!"}

	var msg *logger.Message
	for _, msgString := range msgStrings {
		msg = &logger.Message{Line: []byte(msgString), Source: "stdout", Timestamp: time.Now()}
		err = s.Log(msg)
		assert.Nil(err, "should not fail when calling Log(). sent %s", msgString)
	}

	err = s.Close()
	assert.Nil(err, "should not fail when calling Close()")

	assert.Equal(0, len(messages), "should have received none of the logs")
}
