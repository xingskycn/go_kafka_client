/* Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package go_kafka_client

import (
	"fmt"
	"github.com/Shopify/sarama"
	"sync"
	"testing"
	"time"
)

var numMessages = 1000
var consumeTimeout = 1 * time.Minute
var localZk = "localhost:2181"
var localBroker = "localhost:9092"

func TestConsumerWithInconsistentProducing(t *testing.T) {
	consumeStatus := make(chan int)
	produceMessages := 1
	consumeMessages := 2
	sleepTime := 10 * time.Second
	timeout := 30 * time.Second
	topic := fmt.Sprintf("inconsistent-producing-%d", time.Now().Unix())

	//create topic
	CreateMultiplePartitionsTopic(localZk, topic, 1)
	EnsureHasLeader(localZk, topic)

	Infof("test", "Produce %d message", produceMessages)
	go produceN(t, produceMessages, topic, localBroker)

	config := testConsumerConfig()
	config.Strategy = newCountingStrategy(t, consumeMessages, timeout, consumeStatus)
	consumer := NewConsumer(config)
	Info("test", "Starting consumer")
	go consumer.StartStatic(map[string]int{topic: 1})
	//produce one more message after 10 seconds
	Infof("test", "Waiting for %s before producing another message", sleepTime)
	time.Sleep(sleepTime)
	Infof("test", "Produce %d message", produceMessages)
	go produceN(t, produceMessages, topic, localBroker)

	//make sure we get 2 messages
	if actual := <-consumeStatus; actual != consumeMessages {
		t.Errorf("Failed to consume %d messages within %s. Actual messages = %d", consumeMessages, timeout, actual)
	}

	closeWithin(t, 10*time.Second, consumer)
}

func TestStaticConsumingSinglePartition(t *testing.T) {
	consumeStatus := make(chan int)
	topic := fmt.Sprintf("test-static-%d", time.Now().Unix())

	CreateMultiplePartitionsTopic(localZk, topic, 1)
	EnsureHasLeader(localZk, topic)
	go produceN(t, numMessages, topic, localBroker)

	config := testConsumerConfig()
	config.Strategy = newCountingStrategy(t, numMessages, consumeTimeout, consumeStatus)
	consumer := NewConsumer(config)
	go consumer.StartStatic(map[string]int{topic: 1})
	if actual := <-consumeStatus; actual != numMessages {
		t.Errorf("Failed to consume %d messages within %s. Actual messages = %d", numMessages, consumeTimeout, actual)
	}
	closeWithin(t, 10*time.Second, consumer)
}

func TestStaticConsumingMultiplePartitions(t *testing.T) {
	consumeStatus := make(chan int)
	topic := fmt.Sprintf("test-static-%d", time.Now().Unix())

	CreateMultiplePartitionsTopic(localZk, topic, 5)
	EnsureHasLeader(localZk, topic)
	go produceN(t, numMessages, topic, localBroker)

	config := testConsumerConfig()
	config.Strategy = newCountingStrategy(t, numMessages, consumeTimeout, consumeStatus)
	consumer := NewConsumer(config)
	go consumer.StartStatic(map[string]int{topic: 3})
	if actual := <-consumeStatus; actual != numMessages {
		t.Errorf("Failed to consume %d messages within %s. Actual messages = %d", numMessages, consumeTimeout, actual)
	}
	closeWithin(t, 10*time.Second, consumer)
}

func TestWhitelistConsumingSinglePartition(t *testing.T) {
	consumeStatus := make(chan int)
	timestamp := time.Now().Unix()
	topic1 := fmt.Sprintf("test-whitelist-%d-1", timestamp)
	topic2 := fmt.Sprintf("test-whitelist-%d-2", timestamp)

	CreateMultiplePartitionsTopic(localZk, topic1, 1)
	EnsureHasLeader(localZk, topic1)
	CreateMultiplePartitionsTopic(localZk, topic2, 1)
	EnsureHasLeader(localZk, topic2)
	go produceN(t, numMessages, topic1, localBroker)
	go produceN(t, numMessages, topic2, localBroker)

	expectedMessages := numMessages * 2

	config := testConsumerConfig()
	config.Strategy = newCountingStrategy(t, expectedMessages, consumeTimeout, consumeStatus)
	consumer := NewConsumer(config)
	go consumer.StartWildcard(NewWhiteList(fmt.Sprintf("test-whitelist-%d-.+", timestamp)), 1)
	if actual := <-consumeStatus; actual != expectedMessages {
		t.Errorf("Failed to consume %d messages within %s. Actual messages = %d", expectedMessages, consumeTimeout, actual)
	}
	closeWithin(t, 10*time.Second, consumer)
}

func TestMessagesProcessedOnce(t *testing.T) {
	closeTimeout := 15 * time.Second
	consumeFinished := make(chan bool)
	messages := 100
	topic := fmt.Sprintf("test-processing-%d", time.Now().Unix())
	CreateMultiplePartitionsTopic(localZk, topic, 1)
	EnsureHasLeader(localZk, topic)
	go produceN(t, messages, topic, localBroker)

	config := testConsumerConfig()
	messagesMap := make(map[string]bool)
	var messagesMapLock sync.Mutex
	config.Strategy = func(_ *Worker, msg *Message, id TaskId) WorkerResult {
		value := string(msg.Value)
		inLock(&messagesMapLock, func() {
			if _, exists := messagesMap[value]; exists {
				t.Errorf("Duplicate message: %s", value)
			}
			messagesMap[value] = true
			if len(messagesMap) == messages {
				consumeFinished <- true
			}
		})
		return NewSuccessfulResult(id)
	}
	consumer := NewConsumer(config)

	go consumer.StartStatic(map[string]int{topic: 1})

	select {
	case <-consumeFinished:
	case <-time.After(consumeTimeout):
		t.Errorf("Failed to consume %d messages within %s. Actual messages = %d", messages, consumeTimeout, len(messagesMap))
	}
	closeWithin(t, closeTimeout, consumer)

	//restart consumer
	zkConfig := NewZookeeperConfig()
	zkConfig.ZookeeperConnect = []string{localZk}
	config.Coordinator = NewZookeeperCoordinator(zkConfig)
	consumer = NewConsumer(config)
	go consumer.StartStatic(map[string]int{topic: 1})

	select {
	//this happens if we get a duplicate
	case <-consumeFinished:
		//and this happens normally
	case <-time.After(closeTimeout):
	}
	closeWithin(t, closeTimeout, consumer)
}

func TestSequentialConsuming(t *testing.T) {
	topic := fmt.Sprintf("test-sequential-%d", time.Now().Unix())
	messages := make([]string, 0)
	for i := 0; i < numMessages; i++ {
		messages = append(messages, fmt.Sprintf("test-message-%d", i))
	}
	CreateMultiplePartitionsTopic(localZk, topic, 1)
	EnsureHasLeader(localZk, topic)
	produce(t, messages, topic, localBroker, sarama.CompressionNone)

	config := testConsumerConfig()
	config.NumWorkers = 1
	successChan := make(chan bool)
	config.Strategy = func(_ *Worker, msg *Message, id TaskId) WorkerResult {
		value := string(msg.Value)
		Debug("test", value)
		message := messages[0]
		assert(t, value, message)
		messages = messages[1:]
		if len(messages) == 0 {
			successChan <- true
		}
		return NewSuccessfulResult(id)
	}

	consumer := NewConsumer(config)
	go consumer.StartStatic(map[string]int{topic: 1})

	select {
	case <-successChan:
	case <-time.After(consumeTimeout):
		t.Errorf("Failed to consume %d messages within %s", numMessages, consumeTimeout)
	}
	closeWithin(t, 10*time.Second, consumer)
}

func TestGzipCompression(t *testing.T) {
	testCompression(t, sarama.CompressionGZIP)
}

func TestSnappyCompression(t *testing.T) {
	testCompression(t, sarama.CompressionSnappy)
}

func testCompression(t *testing.T, codec sarama.CompressionCodec) {
	topic := fmt.Sprintf("test-compression-%d", time.Now().Unix())
	messages := make([]string, 0)
	for i := 0; i < numMessages; i++ {
		messages = append(messages, fmt.Sprintf("test-message-%d", i))
	}

	CreateMultiplePartitionsTopic(localZk, topic, 1)
	EnsureHasLeader(localZk, topic)
	produce(t, messages, topic, localBroker, codec)

	config := testConsumerConfig()
	config.NumWorkers = 1
	successChan := make(chan bool)
	config.Strategy = func(_ *Worker, msg *Message, id TaskId) WorkerResult {
		value := string(msg.Value)
		Warn("test", value)
		message := messages[0]
		assert(t, value, message)
		messages = messages[1:]
		if len(messages) == 0 {
			successChan <- true
		}
		return NewSuccessfulResult(id)
	}
	consumer := NewConsumer(config)
	go consumer.StartStatic(map[string]int{topic: 1})

	select {
	case <-successChan:
	case <-time.After(consumeTimeout):
		t.Errorf("Failed to consume %d messages within %s", numMessages, consumeTimeout)
	}
	closeWithin(t, 10*time.Second, consumer)
}

func TestBlueGreenDeployment(t *testing.T) {
	partitions := 2
	activeTopic := fmt.Sprintf("active-%d", time.Now().Unix())
	inactiveTopic := fmt.Sprintf("inactive-%d", time.Now().Unix())

	zkConfig := NewZookeeperConfig()
	zkConfig.ZookeeperConnect = []string{localZk}
	coordinator := NewZookeeperCoordinator(zkConfig)
	coordinator.Connect()

	CreateMultiplePartitionsTopic(localZk, activeTopic, partitions)
	EnsureHasLeader(localZk, activeTopic)
	CreateMultiplePartitionsTopic(localZk, inactiveTopic, partitions)
	EnsureHasLeader(localZk, inactiveTopic)

	blueGroup := fmt.Sprintf("blue-%d", time.Now().Unix())
	greenGroup := fmt.Sprintf("green-%d", time.Now().Unix())

	processedInactiveMessages := 0
	var inactiveCounterLock sync.Mutex

	processedActiveMessages := 0
	var activeCounterLock sync.Mutex

	inactiveStrategy := func(worker *Worker, msg *Message, taskId TaskId) WorkerResult {
		atomicIncrement(&processedInactiveMessages, &inactiveCounterLock)
		return NewSuccessfulResult(taskId)
	}
	activeStrategy := func(worker *Worker, msg *Message, taskId TaskId) WorkerResult {
		atomicIncrement(&processedActiveMessages, &activeCounterLock)
		return NewSuccessfulResult(taskId)
	}
	blueGroupConsumers := []*Consumer{ createConsumerForGroup(blueGroup, inactiveStrategy), createConsumerForGroup(blueGroup, inactiveStrategy) }
	greenGroupConsumers := []*Consumer{ createConsumerForGroup(greenGroup, activeStrategy), createConsumerForGroup(greenGroup, activeStrategy) }

	for _, consumer := range blueGroupConsumers {
		go consumer.StartStatic(map[string]int{
			activeTopic: 1,
		})
	}
	for _, consumer := range greenGroupConsumers {
		go consumer.StartStatic(map[string]int{
			inactiveTopic: 1,
		})
	}

	blue := BlueGreenDeployment{activeTopic, "static", blueGroup}
	green := BlueGreenDeployment{inactiveTopic, "static", greenGroup}

	time.Sleep(15 * time.Second)

	coordinator.RequestBlueGreenDeployment(blue, green)

	time.Sleep(30 * time.Second)

	//All Blue consumers should switch to Green group and change topic to inactive
	greenConsumerIds, _ := coordinator.GetConsumersInGroup(greenGroup)
	for _, consumer := range blueGroupConsumers {
		found := false
		for _, consumerId := range greenConsumerIds {
			if consumerId == consumer.config.Consumerid {
				found = true
			}
		}
		assert(t, found, true)
	}

	//All Green consumers should switch to Blue group and change topic to active
	blueConsumerIds, _ := coordinator.GetConsumersInGroup(blueGroup)
	for _, consumer := range greenGroupConsumers {
		found := false
		for _, consumerId := range blueConsumerIds {
			if consumerId == consumer.config.Consumerid {
				found = true
			}
		}
		assert(t, found, true)
	}

	//At this stage Blue group became Green group
	//and Green group became Blue group

	//Producing messages to both topics
	produceMessages := 10
	Infof(activeTopic, "Produce %d message", produceMessages)
	go produceN(t, produceMessages, activeTopic, localBroker)

	Infof(inactiveTopic, "Produce %d message", produceMessages)
	go produceN(t, produceMessages, inactiveTopic, localBroker)

	time.Sleep(30 * time.Second)

	//Green group consumes from inactive topic
	assert(t, processedInactiveMessages, produceMessages)
	//Blue group consumes from active topic
	assert(t, processedActiveMessages, produceMessages)

	for _, consumer := range blueGroupConsumers {
		closeWithin(t, 30*time.Second, consumer)
	}
	for _, consumer := range greenGroupConsumers {
		closeWithin(t, 30*time.Second, consumer)
	}
}

func testConsumerConfig() *ConsumerConfig {
	config := DefaultConsumerConfig()
	config.AutoOffsetReset = SmallestOffset
	config.WorkerFailureCallback = func(_ *WorkerManager) FailedDecision {
		return CommitOffsetAndContinue
	}
	config.WorkerFailedAttemptCallback = func(_ *Task, _ WorkerResult) FailedDecision {
		return CommitOffsetAndContinue
	}
	config.Strategy = goodStrategy

	zkConfig := NewZookeeperConfig()
	zkConfig.ZookeeperConnect = []string{localZk}
	config.Coordinator = NewZookeeperCoordinator(zkConfig)

	return config
}

func createConsumerForGroup(group string, strategy WorkerStrategy) *Consumer {
	config := testConsumerConfig()
	config.Groupid = group
	config.NumConsumerFetchers = 1
	config.NumWorkers = 1
	config.FetchBatchTimeout = 1
	config.FetchBatchSize = 1
	config.Strategy = strategy

	return NewConsumer(config)
}

func newCountingStrategy(t *testing.T, expectedMessages int, timeout time.Duration, notify chan int) WorkerStrategy {
	consumedMessages := 0
	var consumedMessagesLock sync.Mutex
	consumeFinished := make(chan bool)
	go func() {
		select {
		case <-consumeFinished:
		case <-time.After(timeout):
		}
		inLock(&consumedMessagesLock, func() {
			notify <- consumedMessages
		})
	}()
	return func(_ *Worker, _ *Message, id TaskId) WorkerResult {
		inLock(&consumedMessagesLock, func() {
			consumedMessages++
			if consumedMessages == expectedMessages {
				consumeFinished <- true
			}
		})
		return NewSuccessfulResult(id)
	}
}

func atomicIncrement(counter *int, lock *sync.Mutex) {
	inLock(lock, func() {
		*counter++
	})
}
