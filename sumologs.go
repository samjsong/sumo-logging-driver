// Package sumologs provides the log driver for forwarding server logs to
// SumoLogic HTTP Source endpoint.
package sumologs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/docker/docker/daemon/logger"
	"github.com/docker/docker/daemon/logger/loggerutils"
)

const (
	driverName = "sumologs"
	logOptUrl  = "sumo-url"
	logOptTag  = "tag"

	defaultFrequency  = 5 * time.Second
	defaultBufferSize = 10000
	defaultStreamSize = 4000
	defaultBatchSize = 1000
)

type sumoMessage struct {
	Line   string `json:"line"`
	Host   string `json:"host"`
	Source string `json:"source"`
	Tag    string `json:"tag,omitempty"`
	Time   string `json:"time"`
}

type sumoLogger struct {
	client         *http.Client
	
	httpSourceUrl  string

	frequency      time.Duration
	bufferSize     int
	batchSize      int

	blankMessage   *sumoMessage
	messageStream  chan *sumoMessage
	
	mu             sync.RWMutex
	readyToClose   bool
	closedCond     *sync.Cond
}

func init() {
	if err := logger.RegisterLogDriver(driverName, New); err != nil {
		logrus.Fatal(err)
	}
	if err := logger.RegisterLogOptValidator(driverName, ValidateLogOpt); err != nil {
		logrus.Fatal(err)
	}
}

func New(info logger.Info) (logger.Logger, error) {
	hostname, err := info.Hostname()
	if err != nil {
		return nil, fmt.Errorf("%s: cannot access hostname to set source field", driverName)
	}

	httpSourceUrl, exists := info.Config[logOptUrl]
	if !exists {
		return nil, fmt.Errorf("%s: log-opt '%s' is required", driverName, logOptUrl)
	}

	tag := ""
	if tagTemplate, exists := info.Config[logOptTag]; !exists || tagTemplate != "" {
		tag, err = loggerutils.ParseLogTag(info, loggerutils.DefaultTemplate)
		if err != nil {
			return nil, err
		}
	}

	blankMessage := &sumoMessage{
		Host: hostname,
		Tag: tag,
	}

	// TODO: allow users to configure these variables in future
	frequency := defaultFrequency
	bufferSize := defaultBufferSize
	streamSize := defaultStreamSize
	batchSize := defaultBatchSize

	s := &sumoLogger{
		client: &http.Client{},
		httpSourceUrl: httpSourceUrl,
		blankMessage: blankMessage,
		messageStream: make(chan *sumoMessage, streamSize),
		frequency: frequency,
		bufferSize: bufferSize,
		batchSize: batchSize,
	}

	go s.waitForMessages()

	return s, nil
}

func (s *sumoLogger) waitForMessages() {
	// TODO: Eventually multiline detection should probably happen here...
	// TODO: Discuss design, should we send messages in batches? Fewer http requests
	//		but could potentially send unrelated messages as a single log.
	//		Potential solution would involve changing things on the http-source side,
	//		to parse when unrelated messages are bundled together, but this could
	//		cause complications with multiline detection. Probably easier to just send
	//		one message per log.
	timer := time.NewTicker(s.frequency)
	var messages []*sumoMessage
	for {
		select {
		case <-timer.C:
			messages = s.sendMessages(messages, false)
		case message, open := <-s.messageStream:
			if !open {
				s.sendMessages(messages, true)
				s.mu.Lock()
				defer s.mu.Unlock()
				s.readyToClose = true
				s.closedCond.Signal()
				return
			}
			messages = append(messages, message)
			if len(messages) % s.batchSize == 0 {
				messages = s.sendMessages(messages, false)
			}
		}
	}
}

func (s *sumoLogger) Log(msg *logger.Message) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closedCond != nil {
		return fmt.Errorf("Driver is closed")
	}

	message := *s.blankMessage
	message.Line = string(msg.Line)
	message.Time = fmt.Sprintf("%f", float64(msg.Timestamp.UnixNano())/float64(time.Second))
	message.Source = msg.Source

	s.messageStream <- &message
	logger.PutMessage(msg)
	return nil
}

func (s *sumoLogger) sendMessages(messages []*sumoMessage, driverClosed bool) []*sumoMessage {
	messageCount := len(messages)
	for i := 0; i < messageCount; i += s.batchSize {
		upperBound := i + s.batchSize
		if upperBound > messageCount {
			upperBound = messageCount
		}
		if err := s.trySendMessages(messages[i:upperBound]); err != nil {
			logrus.Error(err)
			if driverClosed || messageCount - i >= s.bufferSize {
				messagesToRetry := s.notifyFailedMessages(messages[i:messageCount], driverClosed)
				return messagesToRetry
			}
			return messages[i:messageCount]
		}
	}
	return messages[:0]
}

func (s *sumoLogger) trySendMessages(messages []*sumoMessage) error {
	if len(messages) == 0 {
		return nil
	}
	var writer bytes.Buffer
	for _, message := range messages {
			jsonEvent, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err := writer.Write(jsonEvent); err != nil {
			return err
		}
	}

	request, err := http.NewRequest("POST", s.httpSourceUrl, bytes.NewBuffer(writer.Bytes()))
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, err := ioutil.ReadAll(response.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: Failed to send event: %s - %s", driverName, response.Status, body)
	}
	return nil
}

func (s *sumoLogger) notifyFailedMessages(messages []*sumoMessage, driverClosed bool) []*sumoMessage {
	messageCount := len(messages)
	var failedMessagesUpperBound int
	var reason string
	if driverClosed {
		failedMessagesUpperBound = messageCount
		reason = "driver was closed"
	} else {
		failedMessagesUpperBound = messageCount - s.bufferSize
		reason = "buffer was full"
	}
	for i := 0; i < failedMessagesUpperBound; i++ {
		logrus.Error(fmt.Errorf("%s: Failed to send message: '%s' in time. REASON: POST request failed, and %s.", driverName, messages[i].Line, reason))
	}
	return messages[failedMessagesUpperBound:messageCount]
}

func ValidateLogOpt(cfg map[string]string) error {
	for key := range cfg {
		switch key {
		case logOptUrl:
			r, err := regexp.Compile("(http|https)://.+/v1/http/.+")
			if err != nil {
				return err
			}
			if !r.MatchString(cfg[key]) {
				return fmt.Errorf("%s: log-opt '%s' must be a valid HTTP source URL")
			}
		case logOptTag:
		case "labels":
		case "env":
		case "env-regex":
		default:
			return fmt.Errorf("%s: Unknown log-opt '%s'", driverName, key)
		}
	}
	return nil
}

func (s *sumoLogger) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closedCond == nil {
		s.closedCond = sync.NewCond(&s.mu)
		close(s.messageStream)
		for !s.readyToClose {
			s.closedCond.Wait()
		}
	}
	return nil
}

func (s *sumoLogger) Name() string {
	return driverName
}

/* 
-----------------------------------------------------------------------------
Changes I made to existing files:
-----------------------------------------------------------------------------
api/swagger.yaml 553
contrib/completion/bash/docker 743 767 803-805 899-904
contrib/completion/zsh/_docker 224 236 247 255
daemon/logdrivers_linux.go 14
daemon/logdrivers_windows.go 12

*/ 
