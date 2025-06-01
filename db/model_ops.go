package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/xos/serverstatus/model"
)

// ServerOps provides operations for managing Server models in BadgerDB
type ServerOps struct {
	db *BadgerDB
}

// NewServerOps creates a new ServerOps instance
func NewServerOps(db *BadgerDB) *ServerOps {
	return &ServerOps{db: db}
}

// GetServer gets a server by ID
func (o *ServerOps) GetServer(id uint64) (*model.Server, error) {
	var server model.Server
	err := o.db.FindModel(id, "server", &server)
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// SaveServer saves a server
func (o *ServerOps) SaveServer(server *model.Server) error {
	return o.db.SaveModel("server", server.ID, server)
}

// DeleteServer deletes a server
func (o *ServerOps) DeleteServer(id uint64) error {
	return o.db.DeleteModel("server", id)
}

// GetAllServers gets all servers
func (o *ServerOps) GetAllServers() ([]*model.Server, error) {
	var servers []*model.Server
	err := o.db.FindAll("server", &servers)
	return servers, err
}

// MonitorHistoryOps provides operations for managing MonitorHistory models in BadgerDB
type MonitorHistoryOps struct {
	db *BadgerDB
}

// NewMonitorHistoryOps creates a new MonitorHistoryOps instance
func NewMonitorHistoryOps(db *BadgerDB) *MonitorHistoryOps {
	return &MonitorHistoryOps{db: db}
}

// SaveMonitorHistory saves a monitor history record
func (o *MonitorHistoryOps) SaveMonitorHistory(history *model.MonitorHistory) error {
	key := fmt.Sprintf("monitor_history:%d:%d", history.MonitorID, history.CreatedAt.UnixNano())
	data, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return o.db.Set(key, data)
}

// GetMonitorHistoriesByMonitorID gets monitor histories for a specific monitor
func (o *MonitorHistoryOps) GetMonitorHistoriesByMonitorID(monitorID uint64, startTime, endTime time.Time) ([]*model.MonitorHistory, error) {
	prefix := fmt.Sprintf("monitor_history:%d", monitorID)
	startKey := fmt.Sprintf("%s:%d", prefix, startTime.UnixNano())
	endKey := fmt.Sprintf("%s:%d", prefix, endTime.UnixNano())

	var histories []*model.MonitorHistory
	err := o.db.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek([]byte(startKey)); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()
			if bytes.Compare(key, []byte(endKey)) > 0 {
				break
			}

			err := item.Value(func(val []byte) error {
				var history model.MonitorHistory
				if err := json.Unmarshal(val, &history); err != nil {
					return err
				}
				histories = append(histories, &history)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	return histories, err
}

// CleanupOldMonitorHistories removes monitor histories older than maxAge
func (o *MonitorHistoryOps) CleanupOldMonitorHistories(maxAge time.Duration) (int, error) {
	// Get all monitor IDs
	serverOps := NewServerOps(o.db)
	servers, err := serverOps.GetAllServers()
	if err != nil {
		return 0, err
	}

	totalDeleted := 0
	for _, server := range servers {
		prefix := fmt.Sprintf("monitor_history:%d", server.ID)
		deleted, err := o.db.CleanupExpiredData(prefix, maxAge)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}

	return totalDeleted, nil
}

// UserOps provides specialized operations for users
type UserOps struct {
	db *BadgerDB
}

// NewUserOps creates a new UserOps instance
func NewUserOps(db *BadgerDB) *UserOps {
	return &UserOps{db: db}
}

// SaveUser saves a user record
func (o *UserOps) SaveUser(user *model.User) error {
	key := fmt.Sprintf("user:%d", user.ID)

	value, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	return o.db.Set(key, value)
}

// GetUserByID gets a user by ID
func (o *UserOps) GetUserByID(id uint64) (*model.User, error) {
	var user model.User

	err := o.db.FindModel(id, "user", &user)
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUserByUsername gets a user by username
func (o *UserOps) GetUserByUsername(username string) (*model.User, error) {
	var users []*model.User

	err := o.db.FindAll("user", &users)
	if err != nil {
		return nil, err
	}

	for _, user := range users {
		if user.Login == username {
			return user, nil
		}
	}

	return nil, ErrorNotFound
}

// DeleteUser deletes a user
func (o *UserOps) DeleteUser(id uint64) error {
	return o.db.Delete(fmt.Sprintf("user:%d", id))
}

// MonitorOps provides specialized operations for monitors
type MonitorOps struct {
	db *BadgerDB
}

// NewMonitorOps creates a new MonitorOps instance
func NewMonitorOps(db *BadgerDB) *MonitorOps {
	return &MonitorOps{db: db}
}

// SaveMonitor saves a monitor record
func (o *MonitorOps) SaveMonitor(monitor *model.Monitor) error {
	key := fmt.Sprintf("monitor:%d", monitor.ID)

	value, err := json.Marshal(monitor)
	if err != nil {
		return fmt.Errorf("failed to marshal monitor: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllMonitors gets all monitors
func (o *MonitorOps) GetAllMonitors() ([]*model.Monitor, error) {
	var monitors []*model.Monitor

	err := o.db.FindAll("monitor", &monitors)
	if err != nil {
		return nil, err
	}

	return monitors, nil
}

// GetMonitorByID gets a monitor by ID
func (o *MonitorOps) GetMonitorByID(id uint64) (*model.Monitor, error) {
	var monitor model.Monitor

	err := o.db.FindModel(id, "monitor", &monitor)
	if err != nil {
		return nil, err
	}

	return &monitor, nil
}

// DeleteMonitor deletes a monitor
func (o *MonitorOps) DeleteMonitor(id uint64) error {
	return o.db.Delete(fmt.Sprintf("monitor:%d", id))
}

// NotificationOps provides specialized operations for notifications
type NotificationOps struct {
	db *BadgerDB
}

// NewNotificationOps creates a new NotificationOps instance
func NewNotificationOps(db *BadgerDB) *NotificationOps {
	return &NotificationOps{db: db}
}

// SaveNotification saves a notification record
func (o *NotificationOps) SaveNotification(notification *model.Notification) error {
	key := fmt.Sprintf("notification:%d", notification.ID)

	value, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllNotifications gets all notifications
func (o *NotificationOps) GetAllNotifications() ([]*model.Notification, error) {
	var notifications []*model.Notification

	err := o.db.FindAll("notification", &notifications)
	if err != nil {
		return nil, err
	}

	return notifications, nil
}

// GetNotificationByID gets a notification by ID
func (o *NotificationOps) GetNotificationByID(id uint64) (*model.Notification, error) {
	var notification model.Notification

	err := o.db.FindModel(id, "notification", &notification)
	if err != nil {
		return nil, err
	}

	return &notification, nil
}

// DeleteNotification deletes a notification
func (o *NotificationOps) DeleteNotification(id uint64) error {
	return o.db.Delete(fmt.Sprintf("notification:%d", id))
}

// CronOps provides specialized operations for cron tasks
type CronOps struct {
	db *BadgerDB
}

// NewCronOps creates a new CronOps instance
func NewCronOps(db *BadgerDB) *CronOps {
	return &CronOps{db: db}
}

// SaveCron saves a cron task record
func (o *CronOps) SaveCron(cron *model.Cron) error {
	key := fmt.Sprintf("cron:%d", cron.ID)

	value, err := json.Marshal(cron)
	if err != nil {
		return fmt.Errorf("failed to marshal cron task: %w", err)
	}

	return o.db.Set(key, value)
}

// GetAllCrons gets all cron tasks
func (o *CronOps) GetAllCrons() ([]*model.Cron, error) {
	var crons []*model.Cron

	err := o.db.FindAll("cron", &crons)
	if err != nil {
		return nil, err
	}

	return crons, nil
}

// GetCronByID gets a cron task by ID
func (o *CronOps) GetCronByID(id uint64) (*model.Cron, error) {
	var cron model.Cron

	err := o.db.FindModel(id, "cron", &cron)
	if err != nil {
		return nil, err
	}

	return &cron, nil
}

// DeleteCron deletes a cron task
func (o *CronOps) DeleteCron(id uint64) error {
	return o.db.Delete(fmt.Sprintf("cron:%d", id))
}
