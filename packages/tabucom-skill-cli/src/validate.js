/** Validate origins that an operator explicitly configured for Tabucom. */
export function normalizeBaseUrl(value) {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error('A non-empty --base-url is required.');
  }

  let url;
  try {
    url = new URL(value.trim());
  } catch {
    throw new Error('TABUCOM_BASE_URL must be a valid absolute URL.');
  }

  if (url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error('TABUCOM_BASE_URL must be an origin without userinfo, path, query, or fragment.');
  }

  const localHTTP = url.protocol === 'http:' &&
    (url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '[::1]');
  if (url.protocol !== 'https:' && !localHTTP) {
    throw new Error('TABUCOM_BASE_URL must use HTTPS, except for localhost or loopback HTTP.');
  }

  return url.origin;
}
