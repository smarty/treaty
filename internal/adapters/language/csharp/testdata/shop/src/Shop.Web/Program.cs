using Shop.App;
using Shop.Adapters;
using Store = Shop.Domain.IOrderStore;

Store store = new SqlOrderStore();
var place = new PlaceOrder(store);
place.Run(1);
