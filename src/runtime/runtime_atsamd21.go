// +build sam,atsamd21

package runtime

import (
	"device/arm"
	"device/sam"
	"machine"
	"unsafe"
)

type timeUnit int64

//go:export Reset_Handler
func main() {
	preinit()
	initAll()
	callMain()
	abort()
}

func init() {
	initClocks()
	initRTC()
	initUARTClock()
	initI2CClock()

	// connect to UART
	machine.UART0.Configure(machine.UARTConfig{})
}

func putchar(c byte) {
	machine.UART0.WriteByte(c)
}

func initClocks() {
	// Set 1 Flash Wait State for 48MHz, required for 3.3V operation according to SAMD21 Datasheet
	sam.NVMCTRL.CTRLB |= (sam.NVMCTRL_CTRLB_RWS_HALF << sam.NVMCTRL_CTRLB_RWS_Pos)

	// Turn on the digital interface clock
	sam.PM.APBAMASK |= sam.PM_APBAMASK_GCLK_
	// turn off RTC
	sam.PM.APBAMASK &^= sam.PM_APBAMASK_RTC_

	// Enable OSC32K clock (Internal 32.768Hz oscillator).
	// This requires registers that are not included in the SVD file.
	// This is from samd21g18a.h and nvmctrl.h:
	//
	// #define NVMCTRL_OTP4 0x00806020
	//
	// #define SYSCTRL_FUSES_OSC32K_CAL_ADDR (NVMCTRL_OTP4 + 4)
	// #define SYSCTRL_FUSES_OSC32K_CAL_Pos 6 /** (NVMCTRL_OTP4) OSC32K Calibration */
	// #define SYSCTRL_FUSES_OSC32K_CAL_Msk (0x7Fu << SYSCTRL_FUSES_OSC32K_CAL_Pos)
	// #define SYSCTRL_FUSES_OSC32K_CAL(value) ((SYSCTRL_FUSES_OSC32K_CAL_Msk & ((value) << SYSCTRL_FUSES_OSC32K_CAL_Pos)))
	// u32_t fuse = *(u32_t *)FUSES_OSC32K_CAL_ADDR;
	// u32_t calib = (fuse & FUSES_OSC32K_CAL_Msk) >> FUSES_OSC32K_CAL_Pos;
	fuse := *(*uint32)(unsafe.Pointer(uintptr(0x00806020) + 4))
	calib := (fuse & uint32(0x7f<<6)) >> 6

	// SYSCTRL_OSC32K_CALIB(calib) |
	//  SYSCTRL_OSC32K_STARTUP(0x6u) |
	//  SYSCTRL_OSC32K_EN32K | SYSCTRL_OSC32K_ENABLE;
	sam.SYSCTRL.OSC32K = sam.RegValue((calib << sam.SYSCTRL_OSC32K_CALIB_Pos) |
		(0x6 << sam.SYSCTRL_OSC32K_STARTUP_Pos) |
		sam.SYSCTRL_OSC32K_EN32K |
		sam.SYSCTRL_OSC32K_EN1K |
		sam.SYSCTRL_OSC32K_ENABLE)
	// Wait for oscillator stabilization
	for (sam.SYSCTRL.PCLKSR & sam.SYSCTRL_PCLKSR_OSC32KRDY) == 0 {
	}

	// Software reset the module to ensure it is re-initialized correctly
	sam.GCLK.CTRL = sam.GCLK_CTRL_SWRST
	// Wait for reset to complete
	for (sam.GCLK.CTRL&sam.GCLK_CTRL_SWRST) > 0 && (sam.GCLK.STATUS&sam.GCLK_STATUS_SYNCBUSY) > 0 {
	}

	// Put OSC32K as source of Generic Clock Generator 1
	sam.GCLK.GENDIV = sam.RegValue((1 << sam.GCLK_GENDIV_ID_Pos) |
		(0 << sam.GCLK_GENDIV_DIV_Pos))
	waitForSync()

	// GCLK_GENCTRL_ID(1) | GCLK_GENCTRL_SRC_OSC32K | GCLK_GENCTRL_GENEN;
	sam.GCLK.GENCTRL = sam.RegValue((1 << sam.GCLK_GENCTRL_ID_Pos) |
		(sam.GCLK_GENCTRL_SRC_OSC32K << sam.GCLK_GENCTRL_SRC_Pos) |
		sam.GCLK_GENCTRL_GENEN)
	waitForSync()

	// Use Generic Clock Generator 1 as source for Generic Clock Multiplexer 0 (DFLL48M reference)
	sam.GCLK.CLKCTRL = sam.RegValue16((sam.GCLK_CLKCTRL_ID_DFLL48 << sam.GCLK_CLKCTRL_ID_Pos) |
		(sam.GCLK_CLKCTRL_GEN_GCLK1 << sam.GCLK_CLKCTRL_GEN_Pos) |
		sam.GCLK_CLKCTRL_CLKEN)
	waitForSync()

	// Remove the OnDemand mode, Bug http://avr32.icgroup.norway.atmel.com/bugzilla/show_bug.cgi?id=9905
	sam.SYSCTRL.DFLLCTRL = sam.SYSCTRL_DFLLCTRL_ENABLE
	// Wait for ready
	for (sam.SYSCTRL.PCLKSR & sam.SYSCTRL_PCLKSR_DFLLRDY) == 0 {
	}

	// Handle DFLL calibration based on info learned from Arduino SAMD implementation,
	// using value stored in fuse.
	// #define SYSCTRL_FUSES_DFLL48M_COARSE_CAL_ADDR (NVMCTRL_OTP4 + 4)
	// #define SYSCTRL_FUSES_DFLL48M_COARSE_CAL_Pos 26           /**< \brief (NVMCTRL_OTP4) DFLL48M Coarse Calibration */
	// #define SYSCTRL_FUSES_DFLL48M_COARSE_CAL_Msk (0x3Fu << SYSCTRL_FUSES_DFLL48M_COARSE_CAL_Pos)
	// #define SYSCTRL_FUSES_DFLL48M_COARSE_CAL(value) ((SYSCTRL_FUSES_DFLL48M_COARSE_CAL_Msk & ((value) << SYSCTRL_FUSES_DFLL48M_COARSE_CAL_Pos)))
	coarse := (fuse >> 26) & 0x3F
	if coarse == 0x3f {
		coarse = 0x1f
	}

	sam.SYSCTRL.DFLLVAL |= sam.RegValue(coarse << sam.SYSCTRL_DFLLVAL_COARSE_Pos)
	sam.SYSCTRL.DFLLVAL |= (0x1ff << sam.SYSCTRL_DFLLVAL_FINE_Pos)

	// Write full configuration to DFLL control register
	// SYSCTRL_DFLLMUL_CSTEP( 0x1f / 4 ) | // Coarse step is 31, half of the max value
	// SYSCTRL_DFLLMUL_FSTEP( 10 ) |
	// SYSCTRL_DFLLMUL_MUL( (48000) ) ;
	sam.SYSCTRL.DFLLMUL = sam.RegValue(((31 / 4) << sam.SYSCTRL_DFLLMUL_CSTEP_Pos) |
		(10 << sam.SYSCTRL_DFLLMUL_FSTEP_Pos) |
		(48000 << sam.SYSCTRL_DFLLMUL_MUL_Pos))

	// disable DFLL
	sam.SYSCTRL.DFLLCTRL = 0
	waitForSync()

	sam.SYSCTRL.DFLLCTRL |= sam.SYSCTRL_DFLLCTRL_MODE |
		sam.SYSCTRL_DFLLCTRL_CCDIS |
		sam.SYSCTRL_DFLLCTRL_USBCRM |
		sam.SYSCTRL_DFLLCTRL_BPLCKC
	// Wait for ready
	for (sam.SYSCTRL.PCLKSR & sam.SYSCTRL_PCLKSR_DFLLRDY) == 0 {
	}

	// Re-enable the DFLL
	sam.SYSCTRL.DFLLCTRL |= sam.SYSCTRL_DFLLCTRL_ENABLE
	// Wait for ready
	for (sam.SYSCTRL.PCLKSR & sam.SYSCTRL_PCLKSR_DFLLRDY) == 0 {
	}

	// Switch Generic Clock Generator 0 to DFLL48M. CPU will run at 48MHz.
	sam.GCLK.GENDIV = sam.RegValue((0 << sam.GCLK_GENDIV_ID_Pos) |
		(0 << sam.GCLK_GENDIV_DIV_Pos))
	waitForSync()

	sam.GCLK.GENCTRL = sam.RegValue((0 << sam.GCLK_GENCTRL_ID_Pos) |
		(sam.GCLK_GENCTRL_SRC_DFLL48M << sam.GCLK_GENCTRL_SRC_Pos) |
		sam.GCLK_GENCTRL_IDC |
		sam.GCLK_GENCTRL_GENEN)
	waitForSync()

	// Modify PRESCaler value of OSC8M to have 8MHz
	sam.SYSCTRL.OSC8M |= (sam.SYSCTRL_OSC8M_PRESC_0 << sam.SYSCTRL_OSC8M_PRESC_Pos)
	sam.SYSCTRL.OSC8M &^= (1 << sam.SYSCTRL_OSC8M_ONDEMAND_Pos)
	// Wait for oscillator stabilization
	for (sam.SYSCTRL.PCLKSR & sam.SYSCTRL_PCLKSR_OSC8MRDY) == 0 {
	}

	// Use OSC8M as source for Generic Clock Generator 3
	sam.GCLK.GENDIV = sam.RegValue((3 << sam.GCLK_GENDIV_ID_Pos))
	waitForSync()

	sam.GCLK.GENCTRL = sam.RegValue((3 << sam.GCLK_GENCTRL_ID_Pos) |
		(sam.GCLK_GENCTRL_SRC_OSC8M << sam.GCLK_GENCTRL_SRC_Pos) |
		sam.GCLK_GENCTRL_GENEN)
	waitForSync()

	// Use OSC32K as source for Generic Clock Generator 2
	// OSC32K/1 -> GCLK2 at 32KHz
	sam.GCLK.GENDIV = sam.RegValue(2 << sam.GCLK_GENDIV_ID_Pos)
	waitForSync()

	sam.GCLK.GENCTRL = sam.RegValue((2 << sam.GCLK_GENCTRL_ID_Pos) |
		(sam.GCLK_GENCTRL_SRC_OSC32K << sam.GCLK_GENCTRL_SRC_Pos) |
		sam.GCLK_GENCTRL_GENEN)
	waitForSync()

	// Use GCLK2 for RTC
	sam.GCLK.CLKCTRL = sam.RegValue16((sam.GCLK_CLKCTRL_ID_RTC << sam.GCLK_CLKCTRL_ID_Pos) |
		(sam.GCLK_CLKCTRL_GEN_GCLK2 << sam.GCLK_CLKCTRL_GEN_Pos) |
		sam.GCLK_CLKCTRL_CLKEN)
	waitForSync()

	// Set the CPU, APBA, B, and C dividers
	sam.PM.CPUSEL = sam.PM_CPUSEL_CPUDIV_DIV1
	sam.PM.APBASEL = sam.PM_APBASEL_APBADIV_DIV1
	sam.PM.APBBSEL = sam.PM_APBBSEL_APBBDIV_DIV1
	sam.PM.APBCSEL = sam.PM_APBCSEL_APBCDIV_DIV1

	// Disable automatic NVM write operations
	sam.NVMCTRL.CTRLB |= sam.NVMCTRL_CTRLB_MANW
}

func initRTC() {
	// turn on digital interface clock
	sam.PM.APBAMASK |= sam.PM_APBAMASK_RTC_

	// disable RTC
	sam.RTC_MODE0.CTRL = 0
	waitForSync()

	// reset RTC
	sam.RTC_MODE0.CTRL |= sam.RTC_MODE0_CTRL_SWRST
	waitForSync()

	// set Mode0 to 32-bit counter (mode 0) with prescaler 1 and GCLK2 is 32KHz/1
	sam.RTC_MODE0.CTRL = sam.RegValue16((sam.RTC_MODE0_CTRL_MODE_COUNT32 << sam.RTC_MODE0_CTRL_MODE_Pos) |
		(sam.RTC_MODE0_CTRL_PRESCALER_DIV1 << sam.RTC_MODE0_CTRL_PRESCALER_Pos) |
		sam.RTC_MODE0_CTRL_MATCHCLR)
	waitForSync()

	sam.RTC_MODE0.COMP0 = 0xffffffff
	waitForSync()

	// re-enable RTC
	sam.RTC_MODE0.CTRL |= sam.RTC_MODE0_CTRL_ENABLE
	waitForSync()

	arm.SetPriority(sam.IRQ_RTC, 0xc0)
	arm.EnableIRQ(sam.IRQ_RTC)
}

func waitForSync() {
	for (sam.GCLK.STATUS & sam.GCLK_STATUS_SYNCBUSY) > 0 {
	}
}

// treat all ticks params coming from runtime as being in microseconds
const tickMicros = 1000

var (
	timestamp        timeUnit // ticks since boottime
	timerLastCounter uint64
)

//go:volatile
type isrFlag bool

var timerWakeup isrFlag

const asyncScheduler = false

// sleepTicks should sleep for d number of microseconds.
func sleepTicks(d timeUnit) {
	for d != 0 {
		ticks() // update timestamp
		ticks := uint32(d)
		timerSleep(ticks)
		d -= timeUnit(ticks)
	}
}

// ticks returns number of microseconds since start.
func ticks() timeUnit {
	// request read of count
	sam.RTC_MODE0.READREQ = sam.RTC_MODE0_READREQ_RREQ
	waitForSync()

	rtcCounter := uint64(sam.RTC_MODE0.COUNT) * 30 // each counter tick == 30.5us
	offset := (rtcCounter - timerLastCounter)      // change since last measurement
	timerLastCounter = rtcCounter
	timestamp += timeUnit(offset) // TODO: not precise
	return timestamp
}

// ticks are in microseconds
func timerSleep(ticks uint32) {
	timerWakeup = false
	if ticks < 30 {
		// have to have at least one clock count
		ticks = 30
	}

	// request read of count
	sam.RTC_MODE0.READREQ = sam.RTC_MODE0_READREQ_RREQ
	waitForSync()

	// set compare value
	cnt := sam.RTC_MODE0.COUNT
	sam.RTC_MODE0.COMP0 = sam.RegValue(uint32(cnt) + (ticks / 30)) // each counter tick == 30.5us
	waitForSync()

	// enable IRQ for CMP0 compare
	sam.RTC_MODE0.INTENSET |= sam.RTC_MODE0_INTENSET_CMP0

	for !timerWakeup {
		arm.Asm("wfi")
	}
}

//go:export RTC_IRQHandler
func handleRTC() {
	// disable IRQ for CMP0 compare
	sam.RTC_MODE0.INTFLAG = sam.RTC_MODE0_INTENSET_CMP0

	timerWakeup = true
}

func initUARTClock() {
	// Turn on clock to SERCOM0 for UART0
	sam.PM.APBCMASK |= sam.PM_APBCMASK_SERCOM0_

	// Use GCLK0 for SERCOM0 aka UART0
	// GCLK_CLKCTRL_ID( clockId ) | // Generic Clock 0 (SERCOMx)
	// GCLK_CLKCTRL_GEN_GCLK0 | // Generic Clock Generator 0 is source
	// GCLK_CLKCTRL_CLKEN ;
	sam.GCLK.CLKCTRL = sam.RegValue16((sam.GCLK_CLKCTRL_ID_SERCOM0_CORE << sam.GCLK_CLKCTRL_ID_Pos) |
		(sam.GCLK_CLKCTRL_GEN_GCLK0 << sam.GCLK_CLKCTRL_GEN_Pos) |
		sam.GCLK_CLKCTRL_CLKEN)
	waitForSync()

	// Turn on clock to SERCOM1 for UART1
	sam.PM.APBCMASK |= sam.PM_APBCMASK_SERCOM1_

	// Use GCLK0 for SERCOM1 aka UART1
	// GCLK_CLKCTRL_ID( clockId ) | // Generic Clock 0 (SERCOMx)
	// GCLK_CLKCTRL_GEN_GCLK0 | // Generic Clock Generator 0 is source
	// GCLK_CLKCTRL_CLKEN ;
	sam.GCLK.CLKCTRL = sam.RegValue16((sam.GCLK_CLKCTRL_ID_SERCOM1_CORE << sam.GCLK_CLKCTRL_ID_Pos) |
		(sam.GCLK_CLKCTRL_GEN_GCLK0 << sam.GCLK_CLKCTRL_GEN_Pos) |
		sam.GCLK_CLKCTRL_CLKEN)
	waitForSync()
}

func initI2CClock() {
	// Turn on clock to SERCOM3 for I2C0
	sam.PM.APBCMASK |= sam.PM_APBCMASK_SERCOM3_

	// Use GCLK0 for SERCOM3 aka I2C0
	// GCLK_CLKCTRL_ID( clockId ) | // Generic Clock 0 (SERCOMx)
	// GCLK_CLKCTRL_GEN_GCLK0 | // Generic Clock Generator 0 is source
	// GCLK_CLKCTRL_CLKEN ;
	sam.GCLK.CLKCTRL = sam.RegValue16((sam.GCLK_CLKCTRL_ID_SERCOM3_CORE << sam.GCLK_CLKCTRL_ID_Pos) |
		(sam.GCLK_CLKCTRL_GEN_GCLK0 << sam.GCLK_CLKCTRL_GEN_Pos) |
		sam.GCLK_CLKCTRL_CLKEN)
	waitForSync()
}
