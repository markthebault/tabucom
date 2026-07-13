import { configPath, readConfig } from './config.js';
import { configure } from './configure.js';
import { install } from './install.js';
import { updateSkill } from './skills.js';

const USAGE = `Usage:
  tabucom-skill install --base-url <url> [--agent <agent>] [--global]
  tabucom-skill configure --base-url <url>
  tabucom-skill status
  tabucom-skill update [--agent <agent>] [--global]`;

function parseOptions(args, allowed) {
  const options = {};
  for (let index = 0; index < args.length; index += 1) {
    const option = args[index];
    if (!allowed.has(option)) throw new Error(`Unknown option: ${option}`);
    if (option === '--global') {
      options.global = true;
      continue;
    }
    const value = args[index + 1];
    if (!value || value.startsWith('--')) throw new Error(`${option} requires a value.`);
    options[option.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

function requireBaseUrl(options) {
  if (!options.baseUrl) throw new Error('install and configure require --base-url <url>.');
}

export async function main(args, dependencies = {}) {
  const output = dependencies.output || console.log;
  const error = dependencies.error || console.error;
  const command = args[0];
  if (!command || command === '--help' || command === '-h' || command === 'help') {
    output(USAGE);
    return command ? 0 : 1;
  }

  try {
    if (command === 'install') {
      const options = parseOptions(args.slice(1), new Set(['--base-url', '--agent', '--global']));
      requireBaseUrl(options);
      return await (dependencies.install || install)(options, { output });
    }
    if (command === 'configure') {
      const options = parseOptions(args.slice(1), new Set(['--base-url']));
      requireBaseUrl(options);
      return await (dependencies.configure || configure)(options.baseUrl, output);
    }
    if (command === 'status') {
      if (args.length !== 1) throw new Error('status does not accept options.');
      const config = await (dependencies.readConfig || readConfig)();
      if (!config) {
        output(`Tabucom is not configured. Run \`tabucom-skill configure --base-url <url>\`.\nConfiguration: ${configPath()}`);
        return 1;
      }
      output(`Configured Tabucom origin: ${config.baseUrl}\nConfiguration: ${configPath()}\nCredentials: environment variables only`);
      return 0;
    }
    if (command === 'update') {
      const options = parseOptions(args.slice(1), new Set(['--agent', '--global']));
      const config = await (dependencies.readConfig || readConfig)();
      if (!config) throw new Error('Tabucom is not configured. Run `tabucom-skill configure --base-url <url>` first.');
      const update = dependencies.updateSkill || updateSkill;
      const code = await update(options);
      if (code === 0) output(`Tabucom skill updated. Configured origin preserved: ${config.baseUrl}`);
      return code;
    }
    throw new Error(`Unknown command: ${command}`);
  } catch (caught) {
    error(`tabucom-skill: ${caught.message}`);
    return 1;
  }
}
