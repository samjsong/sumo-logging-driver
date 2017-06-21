// Package sumologs provides the log driver for forwarding server logs to
// SumoLogic HTTP Source endpoint.
package sumologs

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/docker/docker/daemon/logger"
)

const (
	driverName = "sumologs"
	logOptUrl = "sumo-url"

	defaultFrequency = 5 * time.Second
	defaultBufferSize = 10000
	defaultStreamSize = 4000
)

type sumoMessage struct {
	Line string
}

type sumoLogger struct {
	client    *http.Client
	
	httpSourceUrl string

	frequency  time.Duration
	bufferSize int

	blankMessage  *sumoMessage
	messageStream chan *sumoMessage
	
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
		return nil, fmt.Errorf("%s: log-opt '%s' is required", driverName, logOptUrl)
	}

	blankMessage := &sumoMessage{}

	// TODO: allow users to configure these variables in future
	frequency := defaultFrequency
	bufferSize := defaultBufferSize
	streamSize := defaultStreamSize

	s := &sumoLogger{
		client: &http.Client{},
		httpSourceUrl: httpSourceUrl,
		blankMessage: blankMessage,
		messageStream: make(chan *sumoMessage, streamSize),
		frequency: frequency,
		bufferSize: bufferSize,
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
			messages = s.sendMessages(messages, false)
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

	s.messageStream <- &message
	logger.PutMessage(msg)
	return nil
}

func (s *sumoLogger) sendMessages(messages []*sumoMessage, driverClosed bool) []*sumoMessage {
	messageCount := len(messages)
	for i := 0; i < messageCount; i += 1 {
		if err := s.trySendMessage(messages[i]); err != nil {
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

func (s *sumoLogger) trySendMessage(message *sumoMessage) error {
	request, err := http.NewRequest("POST", s.httpSourceUrl, bytes.NewBuffer([]byte(message.Line)))
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
		reason = "driver is closed"
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
			if cfg[key] == "" {
				return fmt.Errorf("%s: log-opt '%s' cannot be empty", driverName, key)
			}
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
