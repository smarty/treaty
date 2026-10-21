using Shop.Domain;

namespace Shop.Adapters;

public class SqlOrderStore : IOrderStore
{
    private static readonly Dictionary<int, Order> rows;

    static SqlOrderStore()
    {
        rows = new Dictionary<int, Order>();
        Retry.Times(3);
    }

    public void Save(Order order) => rows[order.Id] = order;

    public Order? Find(int id) => rows.GetValueOrDefault(id);

    int IOrderStore.Count => rows.Count + Catalog().Count;

    public List<Product> Catalog() => new();
}
