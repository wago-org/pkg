package store

import (
	"path/filepath"
	"testing"

	"github.com/wago-org/registry-backend/internal/model"
)

func TestNotificationsRoundTrip(t *testing.T) {
	json, err := Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatalf("open json: %v", err)
	}
	pebble, err := OpenPebble(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	defer pebble.Close()

	for _, tc := range []struct {
		name string
		s    Store
	}{{"json", json}, {"pebble", pebble}} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.s
			n, err := s.AddNotification(model.Notification{
				Recipient:    "JTenner",
				Kind:         model.NotifyPublishInvite,
				PackageShort: "workers",
				PackageName:  "github.com/wago-org/workers",
				FromLogin:    "JairusSW",
			})
			if err != nil || n.ID == "" || n.Status != model.NotifyPending || n.CreatedAt == "" {
				t.Fatalf("AddNotification: %+v %v", n, err)
			}

			// Recipient match is case-insensitive and preserves display case.
			inbox := s.NotificationsForRecipient("jtenner")
			if len(inbox) != 1 || inbox[0].Recipient != "JTenner" || inbox[0].Kind != model.NotifyPublishInvite {
				t.Fatalf("NotificationsForRecipient: %+v", inbox)
			}
			if other := s.NotificationsForRecipient("someone-else"); len(other) != 0 {
				t.Fatalf("unexpected notifications for other user: %+v", other)
			}

			// Pending query filters by package + kind + pending status.
			pend := s.PendingNotifications("workers", model.NotifyPublishInvite)
			if len(pend) != 1 {
				t.Fatalf("PendingNotifications: %+v", pend)
			}
			if p2 := s.PendingNotifications("workers", model.NotifyTransfer); len(p2) != 0 {
				t.Fatalf("wrong-kind pending should be empty: %+v", p2)
			}

			// Accepting flips status and drops it from the pending set.
			got, ok := s.SetNotificationStatus(n.ID, model.NotifyAccepted)
			if !ok || got.Status != model.NotifyAccepted || got.ResolvedAt == "" {
				t.Fatalf("SetNotificationStatus: %+v %v", got, ok)
			}
			if p3 := s.PendingNotifications("workers", model.NotifyPublishInvite); len(p3) != 0 {
				t.Fatal("accepted invite should no longer be pending")
			}
			if _, ok := s.SetNotificationStatus("missing", model.NotifyDeclined); ok {
				t.Fatal("status change on a missing notification should fail")
			}
		})
	}
}
