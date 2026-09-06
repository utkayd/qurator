const { execFileSync } = require('node:child_process');
const { mkdirSync } = require('node:fs');
const path = require('node:path');

module.exports = () => {
  mkdirSync(path.join(__dirname, '.bin'), { recursive: true });
  for (const [name, source] of [['qurator', './cmd/qurator'], ['qrdecode', './tools/qrdecode']]) {
    execFileSync('go', ['build', '-trimpath', '-o', path.join(__dirname, '.bin', name), source], {
      cwd: path.resolve(__dirname, '../..'),
      env: { ...process.env, CGO_ENABLED: '0' },
      stdio: 'inherit',
    });
  }
};
