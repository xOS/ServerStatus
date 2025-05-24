// Traffic Data Management Utility Class
class TrafficManager {
    constructor() {
        this.trafficData = {};
        this.lastUpdateTime = 0;
        this.updateInterval = null;
        this.dataVersion = 0;
        this.callbacks = new Set();
    }

    // Parse traffic string to bytes
    static parseTrafficToBytes(trafficStr) {
        if (!trafficStr) return 0;
        const cleanStr = trafficStr.replace(/\s+/g, '').toUpperCase();
        let match = cleanStr.match(/^([\d.,]+)([KMGTPEZY]?B)$/i);
        if (!match) {
            match = cleanStr.match(/^([\d.,]+)([KMGTPEZY])$/i);
            if (match) match[2] = match[2] + 'B';
        }
        if (!match) return 0;
        
        const value = parseFloat(match[1].replace(/,/g, ''));
        const unit = match[2].toUpperCase();
        const units = {
            'B': 1,
            'KB': 1024,
            'MB': 1024 * 1024,
            'GB': 1024 * 1024 * 1024,
            'TB': 1024 * 1024 * 1024 * 1024,
            'PB': 1024 * 1024 * 1024 * 1024 * 1024
        };
        return value * (units[unit] || 1);
    }

    // Format bytes to human readable string
    static formatTrafficSize(bytes, decimals = 2) {
        if (!bytes || isNaN(bytes)) return '0B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return `${parseFloat((bytes / Math.pow(k, i)).toFixed(decimals))}${sizes[i]}`;
    }

    // Standardize traffic unit display
    static standardizeTrafficUnit(trafficStr) {
        if (!trafficStr) return '0B';
        if (/^[\d.,]+\s*[KMGTPEZY]B$/i.test(trafficStr)) return trafficStr;
        const bytes = TrafficManager.parseTrafficToBytes(trafficStr);
        return TrafficManager.formatTrafficSize(bytes);
    }

    // Calculate traffic percentage
    static calculateTrafficPercent(used, max) {
        if (!used || !max) return 0;
        const usedBytes = typeof used === 'number' ? used : TrafficManager.parseTrafficToBytes(used);
        const maxBytes = typeof max === 'number' ? max : TrafficManager.parseTrafficToBytes(max);
        if (maxBytes === 0) return 0;
        return Math.min(100, Math.max(0, Math.round((usedBytes / maxBytes) * 100)));
    }

    // Process raw traffic data
    processTrafficData(rawData) {
        if (!Array.isArray(rawData) || rawData.length === 0) return;

        const newData = {};
        rawData.forEach(item => {
            if (!item || !item.server_id) return;

            const serverId = String(item.server_id);
            const maxTraffic = item.max || '0B';
            const usedTraffic = item.used || '0B';
            
            const standardMax = TrafficManager.standardizeTrafficUnit(maxTraffic);
            const standardUsed = TrafficManager.standardizeTrafficUnit(usedTraffic);
            const maxBytes = TrafficManager.parseTrafficToBytes(maxTraffic);
            const usedBytes = TrafficManager.parseTrafficToBytes(usedTraffic);
            const percent = TrafficManager.calculateTrafficPercent(usedBytes, maxBytes);

            newData[serverId] = {
                max: standardMax,
                used: standardUsed,
                maxBytes: maxBytes,
                usedBytes: usedBytes,
                percent: percent,
                serverName: item.server_name || 'Unknown',
                cycleName: item.cycle_name || 'Default',
                lastUpdate: Date.now()
            };
        });

        this.trafficData = newData;
        this.lastUpdateTime = Date.now();
        this.dataVersion++;
        this.notifySubscribers();
    }

    // Get traffic data for a specific server
    getServerTrafficData(serverId) {
        if (!serverId) return null;
        const serverIdStr = String(serverId).trim();
        
        // Try exact match
        if (this.trafficData[serverIdStr]) {
            return this.trafficData[serverIdStr];
        }

        // Try numeric match
        const numericId = parseInt(serverIdStr);
        if (!isNaN(numericId)) {
            const keys = Object.keys(this.trafficData);
            for (const key of keys) {
                if (parseInt(key) === numericId) {
                    return this.trafficData[key];
                }
            }
        }

        return null;
    }

    // Subscribe to data updates
    subscribe(callback) {
        if (typeof callback === 'function') {
            this.callbacks.add(callback);
        }
    }

    // Unsubscribe from data updates
    unsubscribe(callback) {
        this.callbacks.delete(callback);
    }

    // Notify all subscribers of data updates
    notifySubscribers() {
        this.callbacks.forEach(callback => {
            try {
                callback(this.trafficData, this.dataVersion);
            } catch (e) {
                console.error('Error in traffic data subscriber:', e);
            }
        });
    }

    // Start automatic updates
    startAutoUpdate(interval = 30000) {
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
        }
        this.updateInterval = setInterval(() => {
            if (window.serverTrafficRawData) {
                this.processTrafficData(window.serverTrafficRawData);
            }
        }, interval);
    }

    // Stop automatic updates
    stopAutoUpdate() {
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
            this.updateInterval = null;
        }
    }
}

// Create global instance
window.trafficManager = new TrafficManager();

// Initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    if (window.serverTrafficRawData) {
        window.trafficManager.processTrafficData(window.serverTrafficRawData);
    }
    window.trafficManager.startAutoUpdate();
}); 