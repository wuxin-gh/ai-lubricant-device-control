package dev.devicecontrol.app.command

/**
 * A command failure carrying an explicit wire [code] (protocol v0 §12 closed set).
 *
 * Handlers throw this when the right code is obvious at the call site — e.g. a missing
 * `node_id` arg is `bad_args`, an unknown `press_key` value is `unsupported`. Failures
 * that surface as [dev.devicecontrol.core.CoreException] subclasses or generic JVM
 * exceptions are mapped by [CommandDispatcher.mapException] instead; this class is only
 * for the cases where the mapping is not derivable from the exception type.
 */
class CommandException(
    val code: String,
    override val message: String? = null,
) : Exception(message)
