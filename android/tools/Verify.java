import com.android.apksig.ApkVerifier;
import java.io.File;
public class Verify {
    public static void main(String[] a) throws Exception {
        ApkVerifier.Result r = new ApkVerifier.Builder(new File(a[0])).build().verify();
        System.out.println("verified = " + r.isVerified());
        System.out.println("v1=" + r.isVerifiedUsingV1Scheme()
                + "  v2=" + r.isVerifiedUsingV2Scheme()
                + "  v3=" + r.isVerifiedUsingV3Scheme());
        for (Object t : r.getErrors()) System.out.println("ERR " + t);
        if (!r.getSignerCertificates().isEmpty())
            System.out.println("signer  = " + r.getSignerCertificates().get(0).getSubjectDN());
    }
}
