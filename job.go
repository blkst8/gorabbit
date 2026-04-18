package gorabbit

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"go.uber.org/zap"

	amqp "github.com/rabbitmq/amqp091-go"
)

type Job interface {
	Consume(ctxTimeout time.Duration) error
	Publish(ctx context.Context, msg []byte, options ...PublishOption) error
}

type JobHandler func(ctx context.Context, msg amqp.Delivery) error

type job struct {
	messages    <-chan amqp.Delivery
	channel     *amqp.Channel
	handler     JobHandler
	jobExchange string
	jobQueue    string
	shutdown    chan struct{}
	autoAck     bool
	justPublish bool
}

func (j *job) Consume(ctxTimeout time.Duration) error {
	consumer := fmt.Sprintf(
		"%s-%s",
		j.jobExchange,
		strconv.Itoa(100+rand.Intn(899)),
	)

	var err error
	j.messages, err = j.channel.Consume(
		j.jobQueue,
		consumer,
		j.autoAck,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to consume messages from the queue, err: %w", err)
	}

	go func() {
		shutdown := false
		exit := false
		for !exit {
			select {
			case <-j.shutdown:
				if err := j.channel.Cancel(consumer, false); err != nil {
					logger.Error("error in cancelling consumer", zap.Error(err))
				}
				shutdown = true

			case msg := <-j.messages:
				if len(msg.Body) == 0 && shutdown {
					exit = true
					continue
				}

				if len(msg.Body) == 0 {
					if err := msg.Ack(false); err != nil {
						logger.Error("error in sending ack for empty message", zap.Error(err))
					}
					continue
				}

				ctx := context.Background()
				ctx, cancel := context.WithTimeout(ctx, ctxTimeout)
				handlerErr := j.handler(ctx, msg)
				if handlerErr != nil {
					logger.Error("error in running RabbitMQ handler", zap.Error(handlerErr), zap.Any("body", msg.Body))
				} else {
					logger.Debug("job running successfully")
				}

				cancel()

				if !j.autoAck {
					if handlerErr != nil {
						if err := msg.Nack(false, true); err != nil {
							logger.Error("error in sending nack", zap.Error(err))
						}
					} else {
						if err := msg.Ack(false); err != nil {
							logger.Error("error in sending ack", zap.Error(err))
						}
					}
				}
			}
		}
	}()

	return nil
}

func (j *job) Publish(ctx context.Context, msg []byte, options ...PublishOption) error {
	p := amqp.Publishing{ContentType: "text/json", Body: msg}

	for _, opt := range options {
		opt(&p)
	}

	err := j.channel.PublishWithContext(ctx,
		j.jobExchange,
		j.jobQueue,
		false,
		false,
		p,
	)
	if err != nil {
		return fmt.Errorf("failed to publish the delayed message: %v", err)
	}

	return nil
}

func (r *rabbitMQ) ShutdownJobs() {
	for _, job := range r.jobs {
		job.shutdown <- struct{}{}
	}
}
