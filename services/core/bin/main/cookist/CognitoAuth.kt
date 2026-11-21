package cookist

import org.jose4j.jwa.AlgorithmConstraints
import org.jose4j.jwk.HttpsJwks
import org.jose4j.jws.AlgorithmIdentifiers
import org.jose4j.jwt.MalformedClaimException
import org.jose4j.jwt.consumer.InvalidJwtException
import org.jose4j.jwt.consumer.JwtConsumer
import org.jose4j.jwt.consumer.JwtConsumerBuilder
import org.jose4j.keys.resolvers.HttpsJwksVerificationKeyResolver
import org.jose4j.keys.resolvers.VerificationKeyResolver
import java.security.MessageDigest

internal data class CognitoIdentity(val subject: String)

internal class CognitoAuth private constructor(
    private val consumer: JwtConsumer,
    private val clientIds: Set<String>,
    internal val actorPrefix: String
) {
    internal constructor(issuer: String, clientIds: Set<String>, resolver: VerificationKeyResolver) : this(
        JwtConsumerBuilder()
            .setVerificationKeyResolver(resolver)
            .setJwsAlgorithmConstraints(AlgorithmConstraints.ConstraintType.PERMIT, AlgorithmIdentifiers.RSA_USING_SHA256)
            .setExpectedIssuer(issuer)
            .setSkipDefaultAudienceValidation()
            .setRequireExpirationTime()
            .setRequireSubject()
            .build(),
        clientIds.also { require(it.isNotEmpty() && it.all { id -> id.matches(Regex("[A-Za-z0-9]+")) }) { "Cognito client IDs must be configured" } },
        "cognito:${MessageDigest.getInstance("SHA-256").digest(issuer.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it) }}:"
    )

    fun authenticate(token: String): CognitoIdentity {
        try {
            val claims = consumer.processToClaims(token)
            val subject = claims.subject
            if (claims.getStringClaimValue("token_use") != "access" ||
                claims.getStringClaimValue("client_id") !in clientIds ||
                claims.hasAudience() ||
                subject == null || subject.isBlank() || subject.length > 255 || subject.any { it.isISOControl() }) {
                throw ApiError(401, "unauthorized", "Invalid access token")
            }
            return CognitoIdentity("$actorPrefix$subject")
        } catch (_: InvalidJwtException) {
            throw ApiError(401, "unauthorized", "Invalid access token")
        } catch (_: MalformedClaimException) {
            throw ApiError(401, "unauthorized", "Invalid access token")
        }
    }

    companion object {
        fun create(issuer: String, clientIds: Set<String>): CognitoAuth {
            require(issuer.matches(Regex("https://cognito-idp\\.[a-z0-9-]+\\.amazonaws\\.com/[a-z0-9-]+_[A-Za-z0-9]+"))) {
                "COOKIST_COGNITO_ISSUER must be a Cognito user pool issuer"
            }
            val jwks = HttpsJwks("$issuer/.well-known/jwks.json")
            return CognitoAuth(issuer, clientIds, HttpsJwksVerificationKeyResolver(jwks))
        }
    }
}
