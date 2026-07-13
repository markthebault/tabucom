import { configure } from './configure.js';
import { installSkill } from './skills.js';

export async function install({ baseUrl, agent, global }, dependencies = {}) {
  const configureOrigin = dependencies.configureOrigin || configure;
  const install = dependencies.installSkill || installSkill;
  const output = dependencies.output || console.log;

  await configureOrigin(baseUrl, output);
  const code = await install({ agent, global });
  if (code === 0) output('Tabucom skill installed. It will use the configured origin unless TABUCOM_BASE_URL is set.');
  return code;
}
