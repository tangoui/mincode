public class App {
    static int add(int a, int b) {
        int total = a + b;
        return total;
    }

    public static void main(String[] args) throws InterruptedException {
        System.out.println("add(2, 3) = " + add(2, 3));
        if (args.length > 0 && "--wait".equals(args[0])) {
            new java.util.concurrent.CountDownLatch(1).await();
        }
    }
}
