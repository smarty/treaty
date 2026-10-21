namespace Shop.Adapters;

public class CachedOrderStore : SqlOrderStore
{
    public decimal Cut() => Shop.Domain.Pricing.Discount(1m);
}
