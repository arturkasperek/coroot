import log from '../log.js';

export async function getServerSideProps({ query }) {
  const token = typeof query.token === 'string' ? query.token : '';
  const base = process.env.EXPRESS_URL || 'http://express-demo:3000';
  const url = token ? `${base}/api/chain?token=${encodeURIComponent(token)}` : `${base}/api/chain`;
  log.info(token ? `nextjs chain ${token}` : 'nextjs chain', { url });
  let data = { error: 'unreachable' };
  try {
    const r = await fetch(url);
    data = await r.json();
  } catch (err) {
    log.error('nextjs chain fetch failed', { message: String(err) });
  }
  return { props: { data, token } };
}

export default function Chain({ data, token }) {
  return (
    <main style={{ fontFamily: 'sans-serif', padding: 24 }}>
      <h1>nextjs-demo chain</h1>
      <p>nextjs → express → flask{token ? ` (token ${token})` : ''}.</p>
      <pre>{JSON.stringify(data, null, 2)}</pre>
    </main>
  );
}
