package sumologs

import (
	"bytes"
	// "fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/sirupsen/logrus"

	"github.com/docker/docker/daemon/logger"

	// run "go get gopkg.in/jarcoal/httpmock.v1" before running tests
	"gopkg.in/jarcoal/httpmock.v1"
)

const (
	sumoUrl = "https://stag-events.sumologic.net/receiver/v1/http/ZaVnC4dhaV27p86wEkCthvRI4IUASAom3K3-y2qQI8aLQuMKT8wL4yrXruk4ak1UUk10h4LY7-w9Jcb1yb7a5rSdx2-KkLN48eeyR6eqE17ygZut36dfJQ=="
	sumoUrlMock = "https://good.fake.sumo.Url"
	badUrl = "https://bad.fake.sumo.Url"
)

func captureOutput(f func()) string {
    var buf bytes.Buffer
    logrus.SetOutput(&buf)
    f()
    logrus.SetOutput(os.Stderr)
    return buf.String()
}

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
	t.Run("Missing option: " + logOptUrl, func(t *testing.T) {
		info := logger.Info{
			Config: map[string]string{},
			ContainerID:        "containeriid",
			ContainerName:      "/container_name",
			ContainerImageID:   "contaimageid",
			ContainerImageName: "container_image_name",
		}
		_, err := New(info)

		assert.NotNil(err, "should fail when %s not provided", logOptUrl)
	})
	t.Run("Configured correctly", func(t *testing.T) {
		info := logger.Info{
			Config: map[string]string{
				logOptUrl : sumoUrl,
			},
			ContainerID:        "containeriid",
			ContainerName:      "/container_name",
			ContainerImageID:   "contaimageid",
			ContainerImageName: "container_image_name",
		}

		s, err := New(info)
		assert.Nil(err, "should not fail when configured correctly")
		
		assert.Equal(driverName, s.Name(), "name of logging driver is incorrect")
		
		err = s.Close()
		assert.Nil(err, "should not fail when calling Close() on logging driver")
	})
}

func TestIntegrationTestsDefaultSettings(t *testing.T) {
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

	t.Run("Good URL", func(t *testing.T) {
		info := logger.Info{
			Config: map[string]string{
				logOptUrl : sumoUrlMock,
			},
			ContainerID:        "containeriid",
			ContainerName:      "/container_name",
			ContainerImageID:   "contaimageid",
			ContainerImageName: "container_image_name",
		}

		s, err := New(info)
		assert.Nil(err, "should not fail when calling New()")

		msgStrings := []string{"hello", "", "This a more advanced log, with `$`@`!``(#*)'' and #1!           ", "1234567890"}

		var msg *logger.Message
		for _, msgString := range msgStrings {
			msg = &logger.Message{Line: []byte(msgString), Source: "stdout", Timestamp: time.Now()}
			err = s.Log(msg)
			assert.Nil(err, "should not fail when calling Log(). sent %s", msgString)
		}

		err = s.Close()
		assert.Nil(err, "should not fail when calling Close()")

		expectedMessageCount := len(msgStrings)
		assert.Equal(expectedMessageCount, len(messages), "should have received %d logs", expectedMessageCount)
		for i := 0; i < expectedMessageCount; i++ {
			// assert.Equal(msgStrings[i], messages[i], "message is incorrect")
			assert.Contains(messages[i], "\"" + msgStrings[i] + "\"", "message should contain message string")
		}

		messages = messages[:0]
	})

	t.Run("Bad URL", func(t *testing.T) {
		info := logger.Info{
			Config: map[string]string{
				logOptUrl : badUrl,
			},
			ContainerID:        "containeriid",
			ContainerName:      "/container_name",
			ContainerImageID:   "contaimageid",
			ContainerImageName: "container_image_name",
		}

		s, err := New(info)
		assert.Nil(err, "should not fail when calling New()")

		msgStrings := []string{"hello", "", "This a more advanced log, with `$`@`!``(#*)'' and #1!           "}

		logrusErrors := captureOutput(func() {
			for _, msgString := range msgStrings {
				msg := &logger.Message{Line: []byte(msgString), Source: "stdout", Timestamp: time.Now()}
				err := s.Log(msg)
				assert.Nil(err, "should not fail when calling Log(). sent %s", msgString)
			}
			err = s.Close()
			assert.Nil(err, "should not fail when calling Close()")
		})

		expectedMessageCount := len(msgStrings)
		assert.NotNil(logrusErrors, "should have gotten an output through logrus when trying to log to a bad URL")
		assert.NotEqual(0, strings.Count(logrusErrors, "error"), "should have at least one error message when trying to log to a bad URL")
		assert.Equal(expectedMessageCount, strings.Count(logrusErrors, driverName),
			"should have exactly %d error messages referencing %s, one for each failed message", expectedMessageCount, driverName)

		assert.Equal(0, len(messages), "should have received none of the logs")

		messages = messages[:0]
	})
}
