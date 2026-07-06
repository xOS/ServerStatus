type RangeKey = '24h' | '72h'

export interface FakeMonitorPoint {
  MonitorID: string
  CreatedAt: string
  AvgDelay: number
  Up: number
  Down: number
}

export interface FakeServerInfo {
  ID?: number | string
  id?: number | string
  Name?: string
  Tag?: string
  Host?: Record<string, any>
  State?: Record<string, any>
  LastActive?: string
  IsOnline?: boolean
  is_online?: boolean
  live?: boolean
}

export function makeNetworkHistoryFixture(serverIdValue: string, range: RangeKey): FakeMonitorPoint[] {
  const seed = hashSeed(`${serverIdValue}:${range}`)
  const now = Date.now()
  const hours = range === '72h' ? 72 : 24
  const start = now - hours * 60 * 60 * 1000
  const step = (range === '72h' ? 30 : 15) * 60 * 1000
  const totalSteps = Math.floor((hours * 60 * 60 * 1000) / step)
  const points: FakeMonitorPoint[] = []

  for (let index = 0; index <= totalSteps; index += 1) {
    const time = new Date(start + index * step).toISOString()
    const wave = Math.sin((index + seed) / 7) * 6
    const smallWave = Math.sin((index + seed) / 2.9) * 2.5
    const spike = index % (19 + (seed % 5)) === 0 ? 12 + (seed % 9) : 0
    const cnLoss = index % (31 + (seed % 4)) === 0 ? 1 : 0
    const usLoss = index % (23 + (seed % 5)) === 0 ? 2 : index % 17 === 0 ? 1 : 0
    const euLoss = index % (19 + (seed % 6)) === 0 ? 2 : index % 29 === 0 ? 1 : 0

    points.push(
      {
        MonitorID: 'CN',
        CreatedAt: time,
        AvgDelay: Math.max(8, Math.round(34 + (seed % 16) + wave + smallWave + spike)),
        Up: 20 - cnLoss,
        Down: cnLoss,
      },
      {
        MonitorID: 'US',
        CreatedAt: time,
        AvgDelay: Math.max(18, Math.round(82 + (seed % 23) + wave * 1.7 + smallWave + spike * 1.35)),
        Up: 20 - usLoss,
        Down: usLoss,
      },
      {
        MonitorID: 'EU',
        CreatedAt: time,
        AvgDelay: Math.max(22, Math.round(128 + (seed % 31) + wave * 2.1 + smallWave * 1.4 + spike * 1.1)),
        Up: 20 - euLoss,
        Down: euLoss,
      },
    )
  }

  return points
}

export function appendOfflineServerFixture(list: FakeServerInfo[]): FakeServerInfo[] {
  if (list.some((server) => serverId(server) === '__offline-preview__')) return list

  const source = list.find((server) => server.Host) || null
  const sourceHost = source?.Host || {}
  const sourceState = source?.State || {}
  const gib = 1024 ** 3

  return [
    ...list,
    {
      ID: '__offline-preview__',
      Name: 'Offline Preview',
      Tag: source?.Tag || 'default',
      Host: {
        OS: sourceHost.OS || 'linux',
        Platform: sourceHost.Platform || 'ubuntu',
        PlatformVersion: sourceHost.PlatformVersion || '22.04',
        CPU: Array.isArray(sourceHost.CPU) && sourceHost.CPU.length > 0
          ? sourceHost.CPU
          : ['AMD EPYC Preview CPU'],
        MemTotal: sourceHost.MemTotal || 8 * gib,
        DiskTotal: sourceHost.DiskTotal || 80 * gib,
        SwapTotal: sourceHost.SwapTotal || 2 * gib,
        Arch: sourceHost.Arch || 'x86_64',
        Virtualization: sourceHost.Virtualization || 'kvm',
        BootTime: Math.floor(Date.now() / 1000) - 21 * 86400,
        CountryCode: sourceHost.CountryCode || 'cn',
        Version: sourceHost.Version || 'dev-preview',
        GPU: sourceHost.GPU || [],
      },
      State: {
        CPU: 0,
        MemUsed: 0,
        SwapUsed: 0,
        DiskUsed: 0,
        NetInTransfer: 0,
        NetOutTransfer: 0,
        NetInSpeed: 0,
        NetOutSpeed: 0,
        Uptime: sourceState.Uptime || 0,
        Load1: 0,
        Load5: 0,
        Load15: 0,
        TcpConnCount: 0,
        UdpConnCount: 0,
        ProcessCount: 0,
        Temperatures: [],
      },
      LastActive: new Date(Date.now() - 2 * 86400 * 1000).toISOString(),
      IsOnline: false,
      is_online: false,
      live: false,
    },
  ]
}

function hashSeed(value: string) {
  let hash = 0
  for (let index = 0; index < value.length; index += 1) {
    hash = (hash * 31 + value.charCodeAt(index)) >>> 0
  }
  return hash
}

function serverId(server: FakeServerInfo) {
  return String(server.ID ?? server.id ?? server.Name ?? '')
}
