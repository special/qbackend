#include <QDebug>
#include <QLoggingCategory>
#include <QFile>

#include "qbackendprocess.h"

Q_LOGGING_CATEGORY(lcProcess, "backend.process")

QBackendProcess::QBackendProcess(QObject *parent)
    : QBackendConnection(parent)
{
}

QString QBackendProcess::name() const
{
    return m_name;
}

void QBackendProcess::setName(const QString& name)
{
    if (m_completed) {
        Q_UNREACHABLE();
        return;
    }

    m_name = name;
    emit nameChanged();
}

QStringList QBackendProcess::args() const
{
    return m_args;
}

void QBackendProcess::setArgs(const QStringList& args)
{
    if (m_completed) {
        Q_UNREACHABLE();
        return;
    }

    m_args = args;
    emit argsChanged();
}

void QBackendProcess::classBegin()
{

}

void QBackendProcess::componentComplete()
{
    m_completed = true;

    // Start the process
    m_process.start(m_name, m_args);

    connect(&m_process, &QProcess::stateChanged, this, [=]() {
        qCWarning(lcProcess) << "State changed " << m_process.state();
        if (m_process.state() != QProcess::Running) {
            qCWarning(lcProcess) << m_process.readAllStandardError() << m_process.readAllStandardOutput();
        }
    });

    m_process.waitForStarted();
    setBackendIo(&m_process, &m_process);
}

