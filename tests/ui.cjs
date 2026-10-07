'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../openwrt/root/www/luci-static/resources/view/radarsender/main_v0_1_2.js'), 'utf8');
function element(tag, attrs = {}, children = []) {
    const node = { tag, value: '', disabled: false, textContent: '', children };
    Object.assign(node, attrs);
    if ('value' in attrs) node.value = String(attrs.value);
    return node;
}
async function setup(options = {}) {
    const calls = [];
    let current = { ok: true, state: 'idle', config: {}, has_channel: true, ...options.status };
    const rpc = { declare(spec) { return async (...args) => {
        calls.push({ method: spec.method, args });
        if (spec.method === 'status') return current;
        if (spec.method === 'configure' && options.failSave) return { ok: false, error: '保存失败' };
        if (spec.method === 'start') current = { ...current, state: 'connecting' };
        if (spec.method === 'stop') current = { ...current, state: 'stopping' };
        return { ok: true };
    }; } };
    const app = new Function('view', 'rpc', 'poll', 'E', 'L', source)({ extend: x => x }, rpc, { add() {} }, element, { hasViewPermission: () => !options.readonly });
    app.root = app.render(await app.load());
    return { app, calls };
}
(async () => {
    const { app, calls } = await setup();
    function count(node, tag) {
        if (!node || typeof node !== 'object') return 0;
        const children = Array.isArray(node.children) ? node.children : [node.children];
        return Number(node.tag === tag) + children.reduce((n, child) => n + count(child, tag), 0);
    }
    assert.equal(count(app.root, 'input'), 1);
    assert.equal(count(app.root, 'button'), 1);
    app.channel.value = 'http://192.0.2.10:18880#fixture';
    await Promise.all([app.action(), app.action()]);
    assert.deepEqual(calls.slice(-3).map(c => c.method), ['configure', 'start', 'status']);
    assert.equal(calls.filter(c => c.method === 'start').length, 1);
    assert.deepEqual(calls.find(c => c.method === 'configure').args, ['http://192.0.2.10:18880#fixture']);
    assert.equal(app.channel.value, '');
    assert.equal(app.toggle.textContent, '断开连接');
    assert.equal(app.channel.disabled, true);
    await app.action();
    assert.deepEqual(calls.slice(-2).map(c => c.method), ['stop', 'status']);
    assert.equal(app.toggle.disabled, true);
    app.update({ ok: true, state: 'reconnecting', retry_seconds: 2, error: '网络中断' });
    assert.equal(app.toggle.disabled, false);
    assert.match(app.error.textContent, /2 秒/);
    app.update({ ok: true, state: 'reconnecting', capturing: true, captured: 12, offline_dropped: 12, dropped: 12, retry_seconds: 2 });
    assert.equal(app.state.textContent, '持续采集中，等待服务器');
    assert.match(app.counters.textContent, /已采集 12 包/);
    assert.match(app.counters.textContent, /离线 12/);
    assert.equal(app.toggle.textContent, '断开连接');
    assert.equal(app.toggle.disabled, false);
    const recovery = await setup({ status: { config_error: '配置损坏', has_channel: false } });
    assert.equal(recovery.app.toggle.disabled, false);
    assert.equal(recovery.app.error.textContent, '配置损坏');
    recovery.app.channel.value = 'http://192.0.2.10#fixture';
    await recovery.app.action();
    assert.deepEqual(recovery.calls.slice(-3).map(c => c.method), ['configure', 'start', 'status']);
    const broken = await setup({ status: { ok: false, error: '后台未启动' } });
    assert.equal(broken.app.toggle.disabled, true);
    assert.equal(broken.app.error.textContent, '后台未启动');
    assert.equal(broken.app.channel.disabled, true);
    broken.app.update({ ok: true, state: 'sending', interface: 'br-home' });
    assert.equal(broken.app.lan.textContent, 'LAN（自动识别）：br-home');
    assert.equal(broken.app.toggle.textContent, '断开连接');
    const failed = await setup({ failSave: true });
    failed.app.channel.value = 'http://192.0.2.10#fixture';
    await failed.app.action();
    assert.equal(failed.calls.some(c => c.method === 'start'), false);
    assert.equal(failed.app.channel.value, 'http://192.0.2.10#fixture');
    assert.equal(failed.app.error.textContent, '保存失败');
    const readonly = await setup({ readonly: true });
    await readonly.app.action();
    assert.equal(readonly.calls.length, 1);
    const invalid = await setup({ status: { has_channel: false } });
    await invalid.app.action();
    assert.equal(invalid.calls.length, 1);
    invalid.app.channel.value = 'x'.repeat(4097);
    await invalid.app.action();
    assert.equal(invalid.calls.length, 1);
    const saved = await setup();
    await saved.app.action();
    assert.deepEqual(saved.calls.find(c => c.method === 'configure').args, ['']);
    console.log('PASS UI: exactly one input and button, one-click save/start, duplicate-click guard, disconnect, retry cancellation, repair, detected LAN display, unavailable service, save failure, saved channel reuse, read-only ACL, input bounds');
})().catch(e => { console.error(e); process.exitCode = 1; });
