// Package sumologs provides the log driver for forwarding server logs to
// SumoLogic HTTP Source endpoint.
package sumologs

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/logger"
)

const (
	driverName = "sumologs"
	logOptUrl = "sumo-url"

	defaultFrequency = 5 * time.Second
	defaultBatchSize = 1000
	defaultBufferSize = 10 * defaultBatchSize
	defaultStreamSize = 4 * defaultBatchSize
)

type sumoLogger struct {
	client    *http.Client
	
	httpSourceUrl string

	frequency  time.Duration
	batchSize  int
	bufferSize int
	streamSize int

	messageStream chan string
	mu            sync.RWMutex
	readyToClose  bool
	closedCond    *sync.Cond
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
	httpSourceUrl, ok := info.Config[logOptUrl]
	if !ok {
		return nil, fmt.Errorf("%s: %s is required", driverName, logOptUrl)
	}

	// can allow users to configure these variables in the future
	frequency := defaultFrequency
	batchSize := defaultBatchSize
	bufferSize := defaultBufferSize
	streamSize := defaultStreamSize

	s := &sumoLogger{
		client: &http.Client{},
		httpSourceUrl: httpSourceUrl,
		messageStream: make(chan string, streamSize),
		frequency: frequency,
		batchSize: batchSize,
		bufferSize: bufferSize,
	}

	go s.waitForMessages()

	return s, nil
}

func (s *sumoLogger) waitForMessages() {
	// TODO: Eventually multiline detection should probably happen here...
	//		Might get rid of ticker and send messages immediately instead
	// TODO: Discuss design, should we send messages in batches? Fewer http requests
	//		but could potentially send unrelated messages as a single log.
	//		Potential solution would involve changing things on the http-source side,
	//		to parse when unrelated messages are bundled together, but this could
	//		cause complications with multiline detection. Probably easier to just send
	//		one message per log.
	timer := time.NewTicker(s.frequency)
	var messages []string
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
			messages = s.sendMessages(messages, false)
		}
	}
}

func (s *sumoLogger) Log(msg *logger.Message) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closedCond != nil {
		return fmt.Errorf("%s: driver is closed", driverName)
	}
	s.messageStream <- string(msg.Line)
	logger.PutMessage(msg)
	return nil
}

func (s *sumoLogger) sendMessages(messages []string, driverClosed bool) []string {
	messagesCount := len(messages)
	for i := 0; i < messagesCount; i += 1 {
		if err := s.trySendMessage(messages[i]); err != nil {
			// failed to send the messages
			// TODO: if the driver is closed or the buffer is full, then need to do something with the logs
			logrus.Error(err)
			return messages[i:messagesCount]
		}
	}
	return messages[:0]
}

func (s *sumoLogger) trySendMessage(message string) error {
	req, err := http.NewRequest("POST", s.httpSourceUrl, bytes.NewBuffer([]byte(message)))
	if err != nil {
		return err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var body []byte
		body, err = ioutil.ReadAll(res.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: failed to send event - %s - %s", driverName, res.Status, body)
	}
	io.Copy(ioutil.Discard, res.Body)
	return nil
}

func ValidateLogOpt(cfg map[string]string) error {
	for key := range cfg {
		switch key {
		case logOptUrl:
			if cfg[key] == "" {
				return fmt.Errorf("%s: log-opt %s cannot be empty", driverName, key)
			}
		case "labels":
		case "env":
		case "env-regex":
		default:
			return fmt.Errorf("%s: unknown log-opt '%s'", driverName, key)
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
