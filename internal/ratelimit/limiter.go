// Package ratelimit — токен-бакет для ограничения частоты запросов.
//
// Считаем не «сколько запросов за окно», а «сколько разрешений в бакете»:
// бакет ёмкостью N мгновенно выдерживает N запросов, затем выдаёт по одному
// разрешению каждые refill/N. Так короткие всплески не наказываются, а
// постоянный перебор — блокируется.
//
// Бакеты живут в памяти процесса. За переездом на несколько инстансов это
// станет несогласованным — см. docs/03-UNKNOWN-AND-DECISIONS.md.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter раздаёт разрешения по ключу (IP, логин и т. п.).
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	capacity float64 // максимальное число разрешений
	interval float64 // секунд между выдачами разрешений
	now      func() time.Time
}

type bucket struct {
	tokens   float64
	updated  time.Time
	lastUsed time.Time
}

// New создаёт бакет ёмкостью capacity, который полностью пополняется за refill.
// refill должен быть больше нуля; capacity округляется до целого не меньше 1.
func New(capacity int, refill time.Duration) *Limiter {
	if capacity < 1 {
		capacity = 1
	}
	if refill <= 0 {
		refill = time.Second
	}

	return &Limiter{
		buckets:  make(map[string]*bucket),
		capacity: float64(capacity),
		interval: refill.Seconds() / float64(capacity),
		now:      time.Now,
	}
}

// Allow забирает одно разрешение: true, если лимит не исчерпан.
func (l *Limiter) Allow(key string) bool {
	return l.Consume(key, 1)
}

// Consume забирает n разрешений сразу. Если их не хватает, ничего не списывается
// (иначе неудачная попытка «сжигала» бы остаток лимита просто за факт отказа).
func (l *Limiter) Consume(key string, n int) bool {
	if n < 1 {
		n = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.getLocked(key, now)

	if b.tokens < float64(n) {
		b.lastUsed = now
		return false
	}

	b.tokens -= float64(n)
	b.lastUsed = now
	return true
}

// RetryAfter показывает, сколько ждать следующего разрешения.
// 0 — лимит не исчерпан.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.getLocked(key, now)

	if b.tokens >= 1 {
		return 0
	}

	missing := 1 - b.tokens
	return time.Duration(missing * l.interval * float64(time.Second))
}

// Reset возвращает бакет к полной ёмкости — например после успешного входа,
// чтобы предыдущие неудачные попытки не копились против пользователя.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.buckets, key)
}

// Tokens возвращает текущий остаток разрешений — только для тестов и отладки.
func (l *Limiter) Tokens(key string) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.getLocked(key, l.now()).tokens
}

// getLocked возвращает бакет ключа, пополнив его по прошедшему времени.
// Вызывать только с захваченным мьютексом.
func (l *Limiter) getLocked(key string, now time.Time) *bucket {
	b, ok := l.buckets[key]
	if !ok {
		// Ключ появился — чистим протёкшие, чтобы map не рос бесконечно
		// (перебор с подменой IP иначе оставил бы записи навечно).
		l.pruneLocked(now)
		b = &bucket{tokens: l.capacity, updated: now, lastUsed: now}
		l.buckets[key] = b
		return b
	}

	elapsed := now.Sub(b.updated).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed / l.interval
		if b.tokens > l.capacity {
			b.tokens = l.capacity
		}
		b.updated = now
	}

	return b
}

// pruneLocked удаляет бакеты, полностью пополненные больше окна назад.
// Пополнение ленивое, поэтому «полный ли бакет» считаем по прошедшему времени,
// а не по полю tokens: иначе протёкшая запись никогда не выглядела бы полной
// и map рос бы бесконечно.
func (l *Limiter) pruneLocked(now time.Time) {
	idle := time.Duration(l.interval * float64(time.Second) * l.capacity * 2)

	for key, b := range l.buckets {
		refilled := b.tokens + now.Sub(b.updated).Seconds()/l.interval
		if refilled >= l.capacity && now.Sub(b.lastUsed) > idle {
			delete(l.buckets, key)
		}
	}
}
