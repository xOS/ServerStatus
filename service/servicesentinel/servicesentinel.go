package servicesentinel

import (
	"log"
	"time"

	"github.com/xos/serverstatus/model"
	"github.com/xos/serverstatus/service/singleton"
)

// Sentinel 服务器状态监控哨兵
type Sentinel struct {
	ID    uint64
	State *model.HostState
}

func (s *Sentinel) UpdateStats() {
	// 将监控记录加入数据库
	data := map[string]interface{}{
		"server_id":        s.ID,
		"timestamp":        time.Now().Unix(),
		"cpu":              s.State.CPU,
		"mem_used":         s.State.MemUsed,
		"swap_used":        s.State.SwapUsed,
		"disk_used":        s.State.DiskUsed,
		"net_in_transfer":  s.State.NetInTransfer,
		"net_out_transfer": s.State.NetOutTransfer,
		"net_in_speed":     s.State.NetInSpeed,
		"net_out_speed":    s.State.NetOutSpeed,
		"uptime":           s.State.Uptime,
		"load_1":           s.State.Load1,
		"load_5":           s.State.Load5,
		"load_15":          s.State.Load15,
		"tcp_conn_count":   s.State.TcpConnCount,
		"udp_conn_count":   s.State.UdpConnCount,
		"process_count":    s.State.ProcessCount,
	}

	// 使用批量处理插入监控历史，减少锁竞争
	singleton.AsyncBatchMonitorHistoryInsert(data, func(err error) {
		if err != nil {
			log.Printf("保存监控记录失败 serverID:%d: %v", s.ID, err)
		}
	})
}
