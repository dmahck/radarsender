'use strict';
'require view';
'require rpc';
'require poll';

var status = rpc.declare({ object: 'radarsender', method: 'status', expect: { '': {} } });
var configure = rpc.declare({ object: 'radarsender', method: 'configure', params: ['channel'], expect: { '': {} } });
var start = rpc.declare({ object: 'radarsender', method: 'start', expect: { '': {} } });
var stop = rpc.declare({ object: 'radarsender', method: 'stop', expect: { '': {} } });
var labels = { idle: '未连接', connecting: '正在连接', sending: '正在发送', reconnecting: '等待服务器，自动重连', stopping: '正在断开', error: '已停止，请检查错误' };
function check(result) {
    if (!result || result.ok !== true) throw new Error(result && result.error || '雷达发射后台没有返回有效响应');
    return result;
}
function message(error) { return error && error.message || String(error); }
return view.extend({
    handleSaveApply: null, handleSave: null, handleReset: null,
    load: function() { return status().catch(function(e) { return { ok: false, error: message(e) }; }); },
    render: function(data) {
        var self = this;
        this.busy = false;
        this.readonly = !L.hasViewPermission();
        this.channel = E('input', { id: 'rs-channel', type: 'password', maxlength: 4096, autocomplete: 'new-password', placeholder: '粘贴用户页面提供的完整连接通道', style: 'width:100%' });
        this.state = E('strong'); this.error = E('p', { role: 'alert', style: 'color:#b33;overflow-wrap:anywhere' });
        this.counters = E('p'); this.lan = E('p');
        this.toggle = E('button', { 'class': 'cbi-button cbi-button-action important', click: function() { return self.action(); } }, '连接');
        var root = E('div', { style: 'max-width:960px' }, [
            E('h2', {}, '雷达发射（独立版）'),
            E('p', {}, '粘贴通道后点击连接；自动识别 LAN，全速发送，不设应用速率限制。'),
            E('div', { 'class': 'cbi-section' }, [
                E('label', { 'for': 'rs-channel', style: 'display:block;margin-bottom:8px' }, '连接通道'),
                E('div', { style: 'display:flex;flex-wrap:wrap;gap:10px;align-items:center' }, [
                    E('div', { style: 'flex:1 1 220px;min-width:0' }, [this.channel]), this.toggle
                ])
            ]),
            E('div', { 'class': 'cbi-section' }, [E('h3', {}, '发送状态'), this.state, this.error, this.lan, this.counters]),
            E('p', { 'class': 'description' }, '发送自动识别的 LAN 接口流量，排除自身上传连接。实际速度取决于设备、网络和接收端；队列满时会丢弃副本并计数。流量加速或未经过接口的流量可能漏采。'),
            E('p', { 'class': 'description' }, '服务器离线时保持采集，每次连接失败后 2 秒重试；离线数据丢弃并计数，不补传。手动断开、认证或本地采集故障会结束任务。重启后保持未连接。')
        ]);
        this.update(data);
        poll.add(function() { return self.refresh(); }, 2);
        return root;
    },
    update: function(data) {
        this.data = data || { ok: false };
        var active = ['connecting', 'sending', 'reconnecting', 'stopping'].indexOf(this.data.state) >= 0;
        this.active = active;
        this.state.textContent = this.data.ok ? (labels[this.data.state] || '未知状态') : '后台不可用';
        if (this.data.capturing && ['connecting', 'reconnecting'].indexOf(this.data.state) >= 0) this.state.textContent = '持续采集中，等待服务器';
        this.error.textContent = this.data.config_error || this.data.error || '';
        if (this.data.retry_seconds) this.error.textContent += '；约 ' + this.data.retry_seconds + ' 秒后重试';
        this.counters.textContent = '已采集 ' + (this.data.captured || 0) + ' 包　已发送 ' + (this.data.packets || 0) + ' 包 / ' + ((this.data.bytes || 0) / 1048576).toFixed(2) + ' MiB　丢包 ' + (this.data.dropped || 0) + '（离线 ' + (this.data.offline_dropped || 0) + '）　错误 ' + (this.data.errors || 0);
        this.lan.textContent = this.data.interface ? 'LAN（自动识别）：' + this.data.interface : 'LAN：连接时自动识别';
        this.toggle.textContent = active ? '断开连接' : '连接';
        this.toggle.disabled = this.busy || this.readonly || !this.data.ok || this.data.state === 'stopping';
        this.channel.disabled = this.busy || this.readonly || active || !this.data.ok;
        this.channel.placeholder = this.data.has_channel ? '已保存通道，留空使用；输入新通道可替换' : '粘贴用户页面提供的完整连接通道';
    },
    refresh: function() {
        var self = this;
        return status().then(function(data) { self.update(data); }).catch(function(e) { self.update({ ok: false, error: message(e) }); });
    },
    action: function() {
        var self = this;
        if (this.busy || this.readonly || !this.data.ok || this.data.state === 'stopping') return Promise.resolve();
        var disconnect = this.active, channel = this.channel.value.trim();
        if (!disconnect && ((!channel && !this.data.has_channel) || channel.length > 4096)) { this.error.textContent = '请粘贴用户页面提供的完整连接通道'; return Promise.resolve(); }
        this.busy = true; this.update(this.data);
        var op = disconnect ? stop().then(check) : configure(channel).then(check).then(function() {
            self.channel.value = '';
            return start().then(check);
        });
        return op.then(function() { return self.refresh(); }).catch(function(e) { self.error.textContent = message(e); }).finally(function() {
            self.busy = false;
            self.toggle.disabled = self.readonly || !self.data.ok || self.data.state === 'stopping';
            self.channel.disabled = self.readonly || self.active || !self.data.ok;
        });
    }
});
