import { configPath, writeConfig } from './config.js';

export async function configure(baseUrl, output = console.log) {
  const config = await writeConfig(baseUrl);
  output(`Configured Tabucom origin: ${config.baseUrl}`);
  output(`Configuration: ${configPath()}`);
  output('Credentials remain environment variables and were not written to disk.');
  return 0;
}
