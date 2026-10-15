import log from '../../log.js';

// A catalog page: products 1 to 3 exist, any other id is a 404, so that the
// demo traffic has client errors next to the 2xx and 5xx of the other apps.
const products = {
  1: { name: 'Coffee mug', price: 12 },
  2: { name: 'T-shirt', price: 25 },
  3: { name: 'Sticker pack', price: 5 },
};

export async function getServerSideProps({ params }) {
  const product = products[params.id];
  if (!product) {
    log.warn(`nextjs product ${params.id} not found`);
    return { notFound: true };
  }
  log.info(`nextjs product ${params.id}`);
  return { props: { id: params.id, product } };
}

export default function Product({ id, product }) {
  return (
    <main style={{ fontFamily: 'sans-serif', padding: 24 }}>
      <h1>nextjs-demo product {id}</h1>
      <p>
        {product.name}: ${product.price}
      </p>
    </main>
  );
}
