namespace Shop.Domain;

/// <summary>Where orders live.</summary>
public interface IOrderStore
{
    void Save(Order order);

    Order? Find(int id);

    int Count { get; }
}
