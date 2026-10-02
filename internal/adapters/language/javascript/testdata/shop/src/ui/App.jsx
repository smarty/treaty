import Button from "./Button";
import { Checkout } from "../app/checkout";

export function App({ order }) {
  const checkout = new Checkout(order);
  return (
    <main>
      <Button label={`Pay ${order.id}`} onClick={() => checkout.submit()} />
      <>{order.lines.map((line) => <span key={line.sku}>{line.quantity}</span>)}</>
    </main>
  );
}
