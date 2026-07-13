import { spawn } from 'node:child_process';

const SOURCE = 'markthebault/tabucom';
const SKILL = 'tabucom';

export function addArgs({ agent, global = false } = {}) {
  const args = ['skills', 'add', SOURCE, '--skill', SKILL, '--yes'];
  if (agent) args.push('--agent', agent);
  if (global) args.push('--global');
  return args;
}

export function updateArgs({ global = false } = {}) {
  const args = ['skills', 'update', SKILL, '--yes'];
  if (global) args.push('--global');
  return args;
}

export function runNpx(args, spawnProcess = spawn) {
  return new Promise((resolve, reject) => {
    const child = spawnProcess('npx', args, { stdio: 'inherit', shell: false });
    child.once('error', reject);
    child.once('close', (code, signal) => resolve(code ?? (signal ? 1 : 0)));
  });
}

export async function installSkill(options, run = runNpx) {
  return run(addArgs(options));
}

export async function updateSkill(options, run = runNpx) {
  // The upstream update command filters by scope, but not by agent. Reinstalling
  // is the idempotent, agent-specific update path supported by `skills add`.
  if (options.agent) return run(addArgs(options));

  // skills update works from its lock file. Reinstall if this scope has not been installed yet.
  const code = await run(updateArgs(options));
  return code === 0 ? 0 : run(addArgs(options));
}
