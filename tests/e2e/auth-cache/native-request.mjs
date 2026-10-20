// Direct native verifier probe; inference probes use the real Higress helper.
import http from 'node:http';

export function request(port, host, token, model = 'model-a', body) {
  return new Promise((resolve, reject) => {
    const req = http.request({host: '127.0.0.1', port, path: '/check', method: 'POST', timeout: 5000,
      headers: {host, authorization: 'Bearer ' + token, 'content-type': 'application/json', 'x-aep-model-id': 'model-a'}}, res => {
      res.resume();
      res.on('end', () => {
        try {resolve({status: res.statusCode, observation: JSON.parse(res.headers['x-aep-access-observation'] ?? '{}')});}
        catch (error) {reject(error);}
      });
    });
    req.on('error', reject); req.on('timeout', () => req.destroy(new Error('Native request timeout')));
    req.end(body ?? JSON.stringify({model, messages: [{role: 'user', content: 'disposable probe'}]}));
  });
}
