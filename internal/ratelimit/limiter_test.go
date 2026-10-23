package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// fakeClock — управляемое время, чтобы тесты не спали и не плыли.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestLimiter(capacity int, refill time.Duration) (*Limiter, *fakeClock) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	limiter := New(capacity, refill)
	limiter.now = clock.Now
	return limiter, clock
}

func TestBucketStartsFullAndThenBlocks(t *testing.T) {
	// 3 разрешения за 30 секунд = одно разрешение каждые 10 секунд.
	limiter, _ := newTestLimiter(3, 30*time.Second)

	for i := 0; i < 3; i++ {
		if !limiter.Allow("1.2.3.4") {
			t.Fatalf("запрос %d отклонён, хотя бакет полон", i+1)
		}
	}

	if limiter.Allow("1.2.3.4") {
		t.Fatal("четвёртый запрос подряд должен быть отклонён")
	}
}

func TestRefillIsGradual(t *testing.T) {
	limiter, clock := newTestLimiter(3, 30*time.Second)

	for i := 0; i < 3; i++ {
		limiter.Allow("key")
	}
	if limiter.Allow("key") {
		t.Fatal("лимит должен быть исчерпан")
	}

	clock.Advance(10 * time.Second)
	if !limiter.Allow("key") {
		t.Fatal("через интервал пополнения должно выдать одно разрешение")
	}
	if limiter.Allow("key") {
		t.Fatal("разрешения не должны появляться быстрее интервала")
	}

	clock.Advance(time.Minute)
	if got := limiter.Tokens("key"); got != 3 {
		t.Fatalf("бакет не может переполниться выше ёмкости, получили %v", got)
	}
}

func TestRetryAfterPointsToNextToken(t *testing.T) {
	limiter, clock := newTestLimiter(2, 20*time.Second) // интервал 10s

	limiter.Allow("key")
	limiter.Allow("key")

	if retry := limiter.RetryAfter("key"); retry <= 0 || retry > 10*time.Second {
		t.Fatalf("RetryAfter вне разумных пределов: %v", retry)
	}

	clock.Advance(10 * time.Second)
	if retry := limiter.RetryAfter("key"); retry != 0 {
		t.Fatalf("после пополнения RetryAfter должен быть нулевым, получили %v", retry)
	}
}

func TestConsumeIsAllOrNothing(t *testing.T) {
	limiter, _ := newTestLimiter(3, 30*time.Second)

	if limiter.Consume("key", 5) {
		t.Fatal("нельзя списать больше, чем есть")
	}
	if got := limiter.Tokens("key"); got != 3 {
		t.Fatalf("неудачное списание не должно тратить бакет, осталось %v", got)
	}
	if !limiter.Consume("key", 3) {
		t.Fatal("всё доступное должно списываться одной операцией")
	}
	if limiter.Allow("key") {
		t.Fatal("после полного списания разрешений быть не должно")
	}
}

func TestResetRestoresFullBucket(t *testing.T) {
	limiter, _ := newTestLimiter(2, 20*time.Second)

	limiter.Allow("key")
	limiter.Allow("key")
	if limiter.Allow("key") {
		t.Fatal("бакет пуст")
	}

	limiter.Reset("key")
	if !limiter.Allow("key") || !limiter.Allow("key") {
		t.Fatal("после Reset бакет обязан быть полным")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	limiter, _ := newTestLimiter(1, time.Minute)

	if !limiter.Allow("alice") {
		t.Fatal("alice должна получить первое разрешение")
	}
	if limiter.Allow("alice") {
		t.Fatal("alice исчерпала лимит")
	}
	if !limiter.Allow("bob") {
		t.Fatal("лимит alice не должен влиять на bob")
	}
}

func TestIdleBucketsArePruned(t *testing.T) {
	limiter, clock := newTestLimiter(2, 20*time.Second)

	limiter.Allow("stale")
	if n := len(limiter.buckets); n != 1 {
		t.Fatalf("ожидался 1 бакет, получили %d", n)
	}

	// Окно очистки = interval*capacity*2 = 10s*2*2 = 40s.
	clock.Advance(time.Hour)
	limiter.Allow("fresh")

	if _, ok := limiter.buckets["stale"]; ok {
		t.Fatal("протёкший бакет обязан быть удалён при появлении нового ключа")
	}
	if _, ok := limiter.buckets["fresh"]; !ok {
		t.Fatal("свежий бакет не должен быть удалён")
	}
}

func TestConcurrentAccessKeepsCapacity(t *testing.T) {
	const capacity = 50
	limiter, _ := newTestLimiter(capacity, time.Hour)

	var wg sync.WaitGroup
	allowed := make(chan bool, capacity*4)

	for i := 0; i < capacity*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- limiter.Allow("shared")
		}()
	}

	wg.Wait()
	close(allowed)

	passed := 0
	for ok := range allowed {
		if ok {
			passed++
		}
	}

	if passed != capacity {
		t.Fatalf("сквозь бакет ёмкостью %d прошло %d запросов", capacity, passed)
	}
}
