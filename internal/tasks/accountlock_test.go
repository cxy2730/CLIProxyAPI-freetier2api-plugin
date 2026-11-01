package tasks

import (
	"context"
	"testing"
	"time"
)

// 账号锁的排队语义：批量动作抢的是同一把锁，抢不到必须排队等，
// 不能直接跳过——跳过会让用户看到「该账号有其它任务动作在执行」的假失败。

func TestLockAccountQueuesInsteadOfSkipping(t *testing.T) {
	service := newLifecycleService()
	const uid = "acct-queue"

	if err := service.lockAccount(context.Background(), uid); err != nil {
		t.Fatalf("首次加锁失败：%v", err)
	}
	if service.tryLockAccount(uid) {
		t.Fatal("锁已被占用时 tryLockAccount 不该成功")
	}

	acquired := make(chan struct{})
	go func() {
		_ = service.lockAccount(context.Background(), uid)
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("锁未释放前第二个持有者不该拿到锁")
	case <-time.After(80 * time.Millisecond):
	}

	service.unlockAccount(uid)

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("释放后排队者应立刻拿到锁")
	}
	service.unlockAccount(uid)
}

func TestLockAccountHonoursContextCancel(t *testing.T) {
	service := newLifecycleService()
	const uid = "acct-cancel"

	if err := service.lockAccount(context.Background(), uid); err != nil {
		t.Fatalf("首次加锁失败：%v", err)
	}
	defer service.unlockAccount(uid)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := service.lockAccount(ctx, uid); err == nil {
		t.Fatal("ctx 到期时排队应返回错误，而不是永久阻塞")
	}
}

func TestLockAccountRejectsEmptyUID(t *testing.T) {
	service := newLifecycleService()
	if err := service.lockAccount(context.Background(), "  "); err == nil {
		t.Fatal("空账号标识应直接报错")
	}
	if service.tryLockAccount("") {
		t.Fatal("空账号标识不该抢到锁")
	}
}
