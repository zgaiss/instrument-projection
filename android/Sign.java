import com.android.apksig.ApkSigner;
import java.io.File;
import java.io.FileInputStream;
import java.security.KeyStore;
import java.security.PrivateKey;
import java.security.cert.X509Certificate;
import java.util.Collections;
import java.util.List;

public class Sign {
    public static void main(String[] args) throws Exception {
        String in = args[0], out = args[1], ks = args[2], alias = args[3], pass = args[4];
        KeyStore store = KeyStore.getInstance("PKCS12");
        try (FileInputStream fis = new FileInputStream(ks)) {
            store.load(fis, pass.toCharArray());
        }
        PrivateKey key = (PrivateKey) store.getKey(alias, pass.toCharArray());
        X509Certificate cert = (X509Certificate) store.getCertificate(alias);
        List<X509Certificate> certs = Collections.singletonList(cert);

        ApkSigner.SignerConfig cfg =
                new ApkSigner.SignerConfig.Builder("CERT", key, certs).build();
        new ApkSigner.Builder(Collections.singletonList(cfg))
                .setInputApk(new File(in))
                .setOutputApk(new File(out))
                .setV1SigningEnabled(true)
                .setV2SigningEnabled(true)
                .build()
                .sign();
        System.out.println("SIGNED " + out);
    }
}
