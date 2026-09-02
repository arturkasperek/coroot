import log from '../log.js';

export async function getServerSideProps() {
  const base = process.env.EXPRESS_URL || 'http://express-demo:3000';
  log.info('nextjs fetching express', { url: `${base}/api/hello` });
  let data = { error: 'unreachable' };
  try {
    const r = await fetch(`${base}/api/hello`);
    data = await r.json();
  } catch (err) {
    log.error('nextjs express fetch failed', { message: String(err) });
  }
  return { props: { data } };
}

export default function Home({ data }) {
  return (
    <main style={{ fontFamily: 'sans-serif', padding: 24 }}>
      <h1>nextjs-demo</h1>
      <p>Server-rendered page that calls express-demo (OTLP traces + logs).</p>
      <pre>{JSON.stringify(data, null, 2)}</pre>
    </main>
  );
}
