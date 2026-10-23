using Shop.Adapters;
using static Shop.Domain.Pricing;

namespace Shop.App;

/// <summary>Places an order and stores it.</summary>
public class PlaceOrder(IOrderStore store)
{
    public Result<Order> Run(int id)
    {
        var order = new Order(id) { Customer = "ada" };
        store.Save(order);
        var price = Discount(order.Total(1m));
        return new Result<Order>(order);
    }

    // The deliberate violation: the application reaches a driven adapter.
    public void Audit() => new SqlOrderStore().Save(new Order(0) { Customer = "audit" });
}
