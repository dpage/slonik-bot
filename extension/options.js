const DEFAULT_URL = 'http://127.0.0.1:7381';
const fields = ['serverURL', 'token', 'displayName'];
const result = document.getElementById('result');

const save = () => {
  const values = {};
  for (const f of fields) values[f] = document.getElementById(f).value.trim();
  values.serverURL = values.serverURL || DEFAULT_URL;
  return chrome.storage.local.set(values);
};

(async () => {
  const stored = await chrome.storage.local.get(fields);
  for (const f of fields) document.getElementById(f).value = stored[f] || '';
  document.getElementById('serverURL').value = stored.serverURL || DEFAULT_URL;
})();

for (const f of fields) document.getElementById(f).addEventListener('change', save);

document.getElementById('test').addEventListener('click', async () => {
  await save();
  result.textContent = 'Testing...';
  const res = await chrome.runtime.sendMessage({ type: 'request', method: 'GET', path: '/v1/hello' });
  if (res && res.status === 200) {
    result.textContent = `Connected: slonik-bot ${res.body.version}`;
  } else {
    result.textContent = `Failed: ${(res && res.body && res.body.error) || 'no answer'}`;
  }
});
