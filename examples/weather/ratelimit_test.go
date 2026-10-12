package main

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// clock is a time source a test moves by hand.
type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

func TestTheBucketRefillsAtItsRate(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	limit := newRateLimit(2, 1)
	limit.now = c.read
	limit.last = c.now

	assert.True(t, limit.allow(), "a new bucket starts full")
	assert.False(t, limit.allow(), "the burst is spent")

	c.now = c.now.Add(400 * time.Millisecond)
	assert.False(t, limit.allow(), "0.8 of a token has refilled")

	c.now = c.now.Add(100 * time.Millisecond)
	assert.True(t, limit.allow(), "a whole token has refilled")
}

// An idle bucket refills to its burst and no further, so a quiet hour does not
// bank an hour of requests.
func TestAnIdleBucketHoldsNoMoreThanItsBurst(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	limit := newRateLimit(2, 3)
	limit.now = c.read
	limit.last = c.now

	c.now = c.now.Add(time.Hour)
	for range 3 {
		assert.True(t, limit.allow())
	}
	assert.False(t, limit.allow())
}

// No configuration removes the backstop: a rate that is not a positive finite
// number, or a burst under one, takes the default.
func TestARateOrBurstOutOfRangeTakesTheDefault(t *testing.T) {
	for _, perSecond := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		limit := newRateLimit(perSecond, 0)

		assert.Equal(t, float64(defaultRatePerSecond), limit.perSecond, "rate %v", perSecond)
		assert.Equal(t, float64(defaultRateBurst), limit.burst)
	}
}
